// Package broker carries events over NATS JetStream (design §8).
//
// Every service has one stream, bp_<service>, with subjects
// bp.<service>.<Event>. The emitter creates or updates it at start (its
// configuration wins); a subscriber creates it when missing, with the same
// platform defaults, so it exists before the emitter first runs. Events are
// CloudEvents in NATS binary mode: ce-* headers, JSON data, Nats-Msg-Id =
// ce-id for deduplication and the W3C trace context.
//
// A reactor is a durable pull consumer <subscriber>__<consumer> filtered on
// one event. A handler that fails is redelivered with a growing delay; when
// the last delivery fails the message goes to the dead-letter subject
// bp.dlq.<subscriber>.<consumer> (stream bp_dlq_<subscriber>) and is
// terminated. A consumer deleted on the server while the service runs is
// recreated at the reactor's position (the oldest unsettled message, else
// after the newest one seen) and consumption resumes.
//
// NATS is optional and may be down: Connect does not wait for it, streams
// and consumers are ensured in the background, Publish fails fast while
// disconnected, and consumption resumes after a reconnect.
package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

const instrumentation = "github.com/gopherex/backplane/pkg/backplane/internal/broker"

// ErrNoService: the broker has no service name to derive NATS names from.
var ErrNoService = errors.New("broker: service name is empty")

// Params of New.
type Params struct {
	URL      string
	Creds    string // NATS credentials file content; empty for none
	Service  string
	Instance string
	Version  string
	Log      *xlog.Logger
	Env      *env.Env
}

// tuning holds the timings; tests shorten them.
type tuning struct {
	// Connection.
	reconnectWait   time.Duration
	reconnectJitter time.Duration
	publishTimeout  time.Duration // when the caller's ctx has no deadline
	// Background ensure of streams and consumers.
	retry backoff.Policy
	// Reactors.
	maxDeliver     int
	handlerTimeout time.Duration
	nak            backoff.Policy // delay after the n-th failed delivery
	concurrency    int            // handlers in flight per reactor
}

// Defaults of tuning.
const (
	defReconnectWait   = 2 * time.Second
	defReconnectJitter = time.Second
	defPublishTimeout  = 5 * time.Second
	defRetryFloor      = 500 * time.Millisecond
	defRetryCeil       = 15 * time.Second
	defMaxDeliver      = 5
	defHandlerTimeout  = 30 * time.Second
	defNakFloor        = time.Second
	defNakCeil         = time.Minute
	defConcurrency     = 4
)

func defaults() tuning {
	return tuning{
		reconnectWait:   defReconnectWait,
		reconnectJitter: defReconnectJitter,
		publishTimeout:  defPublishTimeout,
		retry:           backoff.Policy{Min: defRetryFloor, Max: defRetryCeil},
		maxDeliver:      defMaxDeliver,
		handlerTimeout:  defHandlerTimeout,
		nak:             backoff.Policy{Min: defNakFloor, Max: defNakCeil},
		concurrency:     defConcurrency,
	}
}

// Broker is the service's NATS connection: it publishes the service's
// events and runs its reactors.
type Broker struct {
	p   Params
	t   tuning
	log *xlog.Logger

	mu     sync.Mutex
	conn   *nats.Conn
	jet    jetstream.JetStream
	closed chan struct{} // closed when the connection is closed

	reactors reactors
}

// New creates the broker; nothing connects until Connect.
func New(p Params) *Broker {
	log := p.Log
	if log == nil {
		log = xlog.New(xlog.NopCore{})
	}

	return &Broker{p: p, t: defaults(), log: log, closed: make(chan struct{})}
}

// Connect connects without blocking on an unreachable NATS and ensures the
// service's stream; background work runs on g.
func (b *Broker) Connect(ctx context.Context, g node.Group) error {
	if b.p.Service == "" {
		return ErrNoService
	}

	if err := checkService(b.p.Service); err != nil {
		return err
	}

	opts, err := b.options()
	if err != nil {
		return err
	}

	conn, err := nats.Connect(b.p.URL, opts...)
	if err != nil {
		return fmt.Errorf("broker: connect: %w", err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		conn.Close()

		return fmt.Errorf("broker: jetstream: %w", err)
	}

	b.mu.Lock()
	b.conn, b.jet = conn, jet
	b.mu.Unlock()

	if !conn.IsConnected() {
		b.log.Warn("nats unreachable, connecting in the background")
	}

	if b.p.Env == nil || !b.p.Env.Manifest.HasEvents() {
		return nil
	}

	if conn.IsConnected() {
		sctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
		err := b.ensureOwn(sctx)

		cancel()

		if err == nil {
			return nil
		}

		b.log.Warn("event stream not ensured, retrying in the background", xlog.Err(err))
	}

	g.Go(func(ctx context.Context) error {
		err := backoff.Retry(ctx, b.t.retry, b.ensureOwn, func(err error, in time.Duration) {
			b.log.Debug("event stream not ensured", xlog.Err(err), xlog.Duration("retry_in", in))
		})
		if err == nil {
			b.log.Info("event stream ensured", xlog.String("stream", StreamName(b.p.Service)))
		}

		return nil // only the node's stop ends the retries
	})

	return nil
}

// options of the NATS connection: never give up, log the transitions.
func (b *Broker) options() ([]nats.Option, error) {
	name := b.p.Instance
	if name == "" {
		name = b.p.Service
	}

	var opts []nats.Option

	if b.p.Creds != "" {
		creds, err := credentials(b.p.Creds)
		if err != nil {
			return nil, err
		}

		opts = append(opts, creds)
	}

	var once sync.Once

	return append(opts,
		nats.Name(name),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(b.t.reconnectWait),
		nats.ReconnectJitter(b.t.reconnectJitter, b.t.reconnectJitter),
		// No buffering while disconnected: Publish fails instead of waiting.
		nats.ReconnectBufSize(-1),
		nats.ConnectHandler(func(conn *nats.Conn) {
			b.log.Info("nats connected", xlog.String("url", conn.ConnectedUrlRedacted()))
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			b.log.Info("nats reconnected", xlog.String("url", conn.ConnectedUrlRedacted()))
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				b.log.Warn("nats disconnected", xlog.Err(err))
			}
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}

			b.log.Warn("nats error", xlog.Err(err), xlog.String("subject", subject))
		}),
		nats.ClosedHandler(func(*nats.Conn) { once.Do(func() { close(b.closed) }) }),
	), nil
}

// credentials turns the content of a .creds file into the user JWT and
// seed option.
func credentials(content string) (nats.Option, error) {
	jwt, err := nkeys.ParseDecoratedJWT([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("broker: creds: jwt: %w", err)
	}

	kp, err := nkeys.ParseDecoratedUserNKey([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("broker: creds: seed: %w", err)
	}

	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("broker: creds: seed: %w", err)
	}

	return nats.UserJWTAndSeed(jwt, string(seed)), nil
}

// connected returns JetStream while the connection is up.
func (b *Broker) connected() (jetstream.JetStream, error) { //nolint:ireturn // JetStream is only an interface
	b.mu.Lock()
	conn, jet := b.conn, b.jet
	b.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("broker: not connected: %w", env.ErrUnavailable)
	}

	if !conn.IsConnected() {
		return nil, fmt.Errorf("%w (%s): %w", ErrNotConnected, conn.Status(), env.ErrUnavailable)
	}

	return jet, nil
}

// Close drains and closes the connection.
func (b *Broker) Close(ctx context.Context) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()

	if conn == nil {
		return nil
	}

	if err := conn.Drain(); err != nil {
		// Not connected (or already closed): nothing to drain.
		conn.Close()

		return nil //nolint:nilerr // closed either way
	}

	select {
	case <-b.closed:
		return nil
	case <-ctx.Done():
		conn.Close()

		return fmt.Errorf("broker: drain: %w", ctx.Err())
	}
}

// Publish implements env.Broker: event is the full name
// "<service>.<Event>" of one of the service's events, payload its JSON. It
// returns once JetStream acknowledged the event, and fails fast while NATS
// is unreachable.
func (b *Broker) Publish(ctx context.Context, event, key string, payload []byte) error {
	jet, err := b.connected()
	if err != nil {
		return err
	}

	service, name, err := SplitEvent(event)
	if err != nil {
		return err
	}

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, b.t.publishTimeout)
		defer cancel()
	}

	subject := Subject(service, name)
	attrs := xtrace.WithAttrs(
		attribute.String("messaging.system", "nats"),
		attribute.String("messaging.destination.name", subject),
	)

	//nolint:wrapcheck // publish wraps
	return xtrace.Run(ctx, otel.Tracer(instrumentation), "publish "+event, func(ctx context.Context, _ trace.Span) error {
		msg := &nats.Msg{Subject: subject, Data: payload, Header: headers(ctx, cloudEvent{
			ID: uuid.NewString(), Service: service, Instance: b.p.Instance, Version: b.p.Version,
			Type: event, Key: key, Time: time.Now(),
		})}

		return b.publish(ctx, jet, msg, service == b.p.Service)
	}, xtrace.WithSpanOptions(trace.WithSpanKind(trace.SpanKindProducer)), attrs)
}

// publish sends msg and waits for its PubAck. When no stream answers and
// the stream is the service's own, it ensures the stream and tries again.
func (b *Broker) publish(ctx context.Context, jet jetstream.JetStream, msg *nats.Msg, own bool) error {
	_, err := jet.PublishMsg(ctx, msg)
	if errors.Is(err, jetstream.ErrNoStreamResponse) && own {
		if err := b.ensureOwn(ctx); err != nil {
			return err
		}

		_, err = jet.PublishMsg(ctx, msg)
	}

	if err != nil {
		return fmt.Errorf("broker: publish %s: %w", msg.Subject, err)
	}

	return nil
}
