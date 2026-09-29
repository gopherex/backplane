// Package executor is backplane's side of hook calls (design §7.2): it
// answers every hook of the installation over Temporal Nexus and runs
// bindings — and the rules engine's rules — as the workflow
// wire.BindingWorkflow ("backplane.Binding.v1") on backplane's own task
// queue.
//
//   - Endpoints. For every service whose manifests declare hooks there is a
//     Nexus endpoint named after the service, targeting backplane's queue;
//     the executor creates it as soon as the registry shows such a
//     manifest (not when a binding is saved), so a call without a binding
//     gets "no binding", not "endpoint not found". Creation is idempotent
//     across replicas (AlreadyExists is success); an endpoint pointing
//     elsewhere is updated. Endpoints are never deleted: a service that
//     disappears may come back, a caller in flight keeps its route, and an
//     endpoint without calls costs nothing.
//
//   - Nexus handler. Temporal's Go SDK registers Nexus services on a
//     worker before it starts and dispatches by exact service and
//     operation name — there is no catch-all handler — while hook services
//     come and go with the registry. So the handler is a worker of its
//     own: Nexus only (no workflow or activity pollers), on backplane's
//     queue, serving <service>.Hooks with one operation per hook for every
//     hook any known manifest declares. When that set changes the executor
//     starts a new worker with the new set and then stops the old one;
//     backplane's main worker (workflows and activities) is untouched,
//     so its registrations stay static and identical on every replica.
//     The set is derived from the registry alone — every manifest of every
//     service, all versions — so replicas converge on the same one; a new
//     service's endpoint is created only after the local worker serves it
//     and a short settle delay, which covers the other replicas seeing the
//     same registry change.
//
//   - Operation. <Hook> of <service>.Hooks takes HookCall and gives
//     HookResult. The binding in force is compiled against the latest
//     manifests and started as wire.BindingWorkflow, id
//     binding/<hook>/<Nexus request id> (a retried start is the same run),
//     execution timeout = HookCall.deadline; the operation is the run
//     (async). No binding: a required hook fails with a non-retryable
//     application error of type backplane.NoBinding; an optional one
//     answers {} at once.
//
//   - Console. TestBinding, ListBindingRuns, GetBindingRun and
//     CancelBindingRun of BindingService (BindingService wraps the
//     bindings Manager's API with them).
package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc/metadata"

	"github.com/gopherex/xlog"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/bindings"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/workflows"
)

// Errors.
var (
	// ErrNotSynced: the registry has no snapshot yet.
	ErrNotSynced = errors.New("executor: registry not synced yet")
	// ErrUnavailable: Temporal is not configured or not connected.
	ErrUnavailable = errors.New("executor: temporal unavailable")
)

// Defaults.
const (
	// DefaultSettle is how long a new hook service is served before its
	// endpoint is created.
	DefaultSettle = 2 * time.Second
	// DefaultResync is how often endpoints are checked without a registry
	// change (one deleted by hand comes back).
	DefaultResync = time.Minute
	// retryDelay is the next attempt after a failed sync.
	retryDelay = 2 * time.Second
	// defaultNamespace of Temporal.
	defaultNamespace = "default"
	// sessionMetadata carries the console session (§11.1).
	sessionMetadata = "bp-console-session"
)

// Bindings is what the executor reads of bindings: *bindings.Manager.
type Bindings interface {
	// Active is the binding in force and its version; ErrNoBinding.
	Active(ctx context.Context, hook string) (bindings.Binding, int64, error)
	// Version is one version, 0 the current one.
	Version(ctx context.Context, hook string, version int64) (bindings.BindingVersion, error)
}

// TemporalFunc gives the Temporal client.
type TemporalFunc func() (client.Client, error)

// Option configures New.
type Option func(*Executor)

// Namespace of Temporal the endpoints target (default "default"): the
// SDK's BACKPLANE_TEMPORAL_NS.
func Namespace(ns string) Option { return func(x *Executor) { x.ns = ns } }

// WithTemporal replaces the SDK's Temporal client and backplane's task
// queue (tests).
func WithTemporal(fn TemporalFunc, queue string) Option {
	return func(x *Executor) { x.temporal, x.queue = fn, queue }
}

// Author names who made a console call from its context; default: the
// console session as console:<id>, else "admin".
func Author(fn func(ctx context.Context) string) Option { return func(x *Executor) { x.author = fn } }

// Runs is how runs are listed, read and canceled: ops' WorkflowService
// (visibility, history summaries). Without it those RPCs are UNAVAILABLE.
func Runs(runs consolev1.WorkflowServiceServer) Option { return func(x *Executor) { x.runs = runs } }

// Settle replaces DefaultSettle (tests: 0).
func Settle(d time.Duration) Option { return func(x *Executor) { x.settle = d } }

// Resync replaces DefaultResync.
func Resync(d time.Duration) Option { return func(x *Executor) { x.resync = d } }

// Executor is the component: the Nexus worker, the endpoints, the
// compile cache. It holds state and goroutines: share it by pointer.
type Executor struct {
	deps.Component

	bindings Bindings
	src      registry.Source
	temporal TemporalFunc
	queue    string
	ns       string
	author   func(ctx context.Context) string
	runs     consolev1.WorkflowServiceServer
	settle   time.Duration
	resync   time.Duration
	metrics  metrics

	mu       sync.Mutex
	served   hookSet       // what worker serves
	worker   worker.Worker // the Nexus worker; nil while nothing is served
	stopped  bool
	programs map[string]compiled // by hook
	warned   map[string]bool     // services whose name cannot be an endpoint
}

// New creates the component under parent: bindings from b, manifests
// from src, Temporal through the SDK's client. Give RegisterWorkflows to
// workflows.Register on the same root.
func New(parent deps.Scope, b Bindings, src registry.Source, opts ...Option) *Executor {
	x := &Executor{
		Component: deps.NewComponent(parent, "executor"),
		bindings:  b, src: src, ns: defaultNamespace, author: sessionAuthor,
		settle: DefaultSettle, resync: DefaultResync,
		programs: map[string]compiled{}, warned: map[string]bool{},
	}
	x.temporal = func() (client.Client, error) { return workflows.Client(x) }
	x.queue = workflows.Queue(parent)

	for _, o := range opts {
		o(x)
	}

	x.metrics = newMetrics(x.Meter())

	x.Go(x.follow)
	x.OnStop(x.stop)

	return x
}

// RegisterWorkflows adds wire.BindingWorkflow to backplane's worker (give
// it to workflows.Register).
func (x *Executor) RegisterWorkflows(r worker.Registry) { registerWorkflow(r, x.metrics) }

// Queue is backplane's task queue: the endpoints' target, where runs go.
func (x *Executor) Queue() string { return x.queue }

// client is the Temporal client or ErrUnavailable.
func (x *Executor) client() (client.Client, error) {
	c, err := x.temporal()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return c, nil
}

// follow keeps the Nexus worker and the endpoints in step with the
// registry: on every change, every resync, and again shortly after a
// failure.
func (x *Executor) follow(ctx context.Context) error {
	changes := x.src.Changes(ctx)

	for {
		wait := x.resync

		if err := x.Sync(ctx); err != nil {
			wait = retryDelay

			switch {
			case ctx.Err() != nil, errors.Is(err, ErrNotSynced):
			case errors.Is(err, ErrUnavailable):
				x.Log().Debug("executor waits for temporal", xlog.Err(err))
			default:
				x.Log().Warn("executor sync failed, retrying", xlog.Err(err), xlog.Duration("retry_in", wait))
			}
		}

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil
		case _, ok := <-changes:
			timer.Stop()

			if !ok {
				return nil
			}
		case <-timer.C:
		}
	}
}

// Sync makes the Nexus worker serve the hooks of the current registry
// snapshot and ensures their endpoints. follow runs it; tests call it.
func (x *Executor) Sync(ctx context.Context) error {
	cat := x.src.Current()
	if cat.Index == 0 {
		return ErrNotSynced
	}

	c, err := x.client()
	if err != nil {
		return err
	}

	set := hooksOf(cat)

	added, err := x.serve(ctx, c, set)
	if err != nil {
		return err
	}

	if len(added) > 0 && x.settle > 0 {
		timer := time.NewTimer(x.settle)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("executor: %w", ctx.Err())
		case <-timer.C:
		}
	}

	return x.ensureEndpoints(ctx, c, set)
}

// stop stops the Nexus worker within ctx.
func (x *Executor) stop(ctx context.Context) error {
	x.mu.Lock()
	w := x.worker
	x.worker, x.served, x.stopped = nil, nil, true
	x.mu.Unlock()

	if w == nil {
		return nil
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		w.Stop()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		x.Log().Warn("nexus worker still stopping at the end of the stop budget")
	}

	return nil
}

// sessionAuthor: the console session of the request, else "admin".
func sessionAuthor(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(sessionMetadata); len(v) > 0 && v[0] != "" {
			return "console:" + v[0]
		}
	}

	return bindings.DefaultAuthor
}

// audit logs a mutating console call: who, what, on what.
func (x *Executor) audit(ctx context.Context, action, subject string, fields ...xlog.Field) {
	fs := append([]xlog.Field{
		xlog.String("actor", x.author(ctx)), xlog.String("action", action), xlog.String("subject", subject),
	}, fields...)
	x.Log().Ctx().Info(ctx, "console action", fs...)
}
