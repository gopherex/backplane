// Package temporal runs the service's activities and hook calls on
// Temporal (design §7.2, §9).
//
// Names: task queue = service name; activity type = activity name as
// declared; hook = Nexus operation <Name> of Nexus service <service>.Hooks
// on endpoint <service>. A hook raised outside workflow code runs through
// the short workflow backplane.CallHook on the service's own queue, id
// hook/<service>/<Name>/<uuid>.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	otelsdk "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

const (
	defaultNamespace = "default"
	// dialTimeout bounds one connection attempt; Connect makes one before
	// going to the background.
	dialTimeout = 2 * time.Second
	// DefaultHookTimeout bounds a hook call whose context has no deadline.
	DefaultHookTimeout = 30 * time.Second
	// stopGrace is how long a stopping worker lets activities finish; the
	// stop budget of the service bounds it in practice.
	stopGrace = time.Minute
	scopeName = "github.com/gopherex/backplane/temporal"
	// Bounds of the delay between connection and worker start attempts.
	retryFloor = time.Second
	retryCeil  = 30 * time.Second
)

// Params of New.
type Params struct {
	Addr      string
	Namespace string // default "default"
	Service   string
	Instance  string
	Log       *xlog.Logger
	Env       *env.Env
}

// Client is the service's Temporal connection and worker.
type Client struct {
	p      Params
	log    *xlog.Logger
	retry  backoff.Policy // between connection and worker start attempts
	tracer interceptor.Interceptor

	mu      sync.Mutex
	client  client.Client // nil until connected
	lastErr error         // of the last failed dial
	closed  bool
	worker  worker.Worker // nil until started
	stopped bool          // StopWorker ran: a late start is undone
	ready   chan struct{} // closed once client is set

	endpoint atomic.Bool // the service's Nexus endpoint was seen
}

// New creates the client; nothing connects until Connect.
func New(p Params) *Client {
	if p.Namespace == "" {
		p.Namespace = defaultNamespace
	}

	log := p.Log
	if log == nil {
		log = xlog.New(nil)
	}

	return &Client{
		p: p, log: log.With(xlog.String("component", "temporal")),
		retry: backoff.Policy{Min: retryFloor, Max: retryCeil}, ready: make(chan struct{}),
	}
}

// Connect dials without blocking on an unreachable Temporal: after one short
// attempt it keeps retrying on g and returns. Until connected, Call fails
// fast with env.ErrUnavailable and the worker waits.
func (c *Client) Connect(ctx context.Context, g node.Group) error {
	tracer, err := otelsdk.NewTracingInterceptor(otelsdk.TracerOptions{})
	if err != nil {
		return fmt.Errorf("temporal: tracing: %w", err)
	}

	c.tracer = tracer

	if err := c.dial(ctx); err == nil {
		return nil
	}

	c.log.Warn("temporal unreachable, connecting in background", xlog.String("addr", c.p.Addr), xlog.Err(c.dialErr()))

	g.Go(func(ctx context.Context) error {
		err := backoff.Retry(ctx, c.retry, c.dial, func(err error, in time.Duration) {
			c.log.Debug("temporal unreachable", xlog.Err(err), xlog.Duration("retry_in", in))
		})
		if err == nil {
			c.log.Info("temporal connected", xlog.String("addr", c.p.Addr))
		}

		return nil
	})

	return nil
}

// dial makes one connection attempt.
func (c *Client) dial(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := client.DialContext(ctx, c.options())

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case err != nil:
		c.lastErr = err

		return fmt.Errorf("temporal: dial %s: %w", c.p.Addr, err)
	case c.closed:
		conn.Close()

		return nil
	default:
		c.client, c.lastErr = conn, nil
		close(c.ready)

		return nil
	}
}

func (c *Client) options() client.Options {
	log := c.log

	return client.Options{
		HostPort:     c.p.Addr,
		Namespace:    c.p.Namespace,
		Identity:     c.p.Instance,
		Logger:       tlog.NewStructuredLogger(slog.New(xlog.NewSlogHandler(log))),
		Interceptors: []interceptor.ClientInterceptor{c.tracer},
		MetricsHandler: otelsdk.NewMetricsHandler(otelsdk.MetricsHandlerOptions{
			Meter:                otel.GetMeterProvider().Meter(scopeName),
			OnError:              func(err error) { log.Debug("temporal metric", xlog.Err(err)) },
			UseMonotonicCounters: true,
		}),
	}
}

func (c *Client) dialErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastErr
}

// current is the connected client or nil.
func (c *Client) current() client.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.client
}

// SDK is the connected Temporal client for the author's own workflows;
// ErrUnavailable while not connected.
func (c *Client) SDK() (client.Client, error) {
	if conn := c.current(); conn != nil {
		return conn, nil
	}

	if err := c.dialErr(); err != nil {
		return nil, fmt.Errorf("temporal: %w: %w", env.ErrUnavailable, err)
	}

	return nil, fmt.Errorf("temporal: not connected yet: %w", env.ErrUnavailable)
}

// Close closes the connection; a dial still in flight is dropped.
func (c *Client) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.client != nil {
		c.client.Close()
	}

	return nil
}

// Call implements env.Caller: a hook call from outside a workflow. It runs
// the workflow backplane.CallHook on the service's own queue, which raises
// the Nexus operation, and waits for it within ctx (DefaultHookTimeout when
// ctx has no deadline).
func (c *Client) Call(ctx context.Context, hook string, in []byte) ([]byte, error) {
	conn := c.current()
	if conn == nil {
		if err := c.dialErr(); err != nil {
			return nil, fmt.Errorf("temporal unreachable: %w: %w", env.ErrUnavailable, err)
		}

		return nil, fmt.Errorf("temporal not connected: %w", env.ErrUnavailable)
	}

	name, ok := strings.CutPrefix(hook, c.p.Service+".")
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a hook of %s", errHookName, hook, c.p.Service)
	}

	timeout := DefaultHookTimeout
	if d, set := ctx.Deadline(); set {
		timeout = time.Until(d)
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := c.checkEndpoint(ctx, conn); err != nil {
		return nil, err
	}

	call := &backplanev1.HookCall{
		Hook: hook, Instance: c.p.Instance, Payload: in, Trace: Inject(ctx), Deadline: durationpb.New(timeout),
	}

	run, err := conn.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       "hook/" + c.p.Service + "/" + name + "/" + uuid.NewString(),
		TaskQueue:                c.p.Service,
		WorkflowExecutionTimeout: timeout,
	}, CallHookWorkflow, call)
	if err != nil {
		return nil, callError(ctx, fmt.Errorf("start: %w", err))
	}

	var res backplanev1.HookResult
	if err := run.Get(ctx, &res); err != nil {
		return nil, callError(ctx, err)
	}

	return res.GetPayload(), nil
}

var errHookName = errors.New("temporal: bad hook name")

// checkEndpoint makes sure backplane registered the service's Nexus
// endpoint before a call that would otherwise wait out its deadline on a
// missing one. Once seen it is not checked again; a server that refuses the
// check (no operator permission) lets the call go.
func (c *Client) checkEndpoint(ctx context.Context, conn client.Client) error {
	if c.endpoint.Load() {
		return nil
	}

	res, err := conn.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{
		Name: c.p.Service, PageSize: 1,
	})

	switch {
	case err != nil:
		c.log.Debug("nexus endpoint not checked", xlog.Err(err))

		return nil
	case len(res.GetEndpoints()) == 0:
		return fmt.Errorf("no Nexus endpoint %q, backplane has not registered the service: %w",
			c.p.Service, env.ErrUnavailable)
	}

	c.endpoint.Store(true)

	return nil
}

// callError: the caller's deadline or cancellation wins over whatever
// Temporal reported at that moment.
func callError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w", context.Cause(ctx))
	}

	return Local(err)
}
