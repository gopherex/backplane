package broker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

const (
	// ackMargin: the server redelivers an unacknowledged message only after
	// the handler timeout has surely passed.
	ackMargin = 15 * time.Second
)

// Errors of reactors.
var (
	// ErrHandlerPanic: a reactor's handler panicked.
	ErrHandlerPanic = errors.New("broker: reactor handler panicked")
	// ErrStopTimeout: handlers in flight did not finish within the stop
	// budget and were cancelled.
	ErrStopTimeout = errors.New("broker: reactor handlers did not finish in time")
)

// reactors is the state of the running reactors.
type reactors struct {
	mu       sync.Mutex
	stopping bool
	stop     chan struct{}      // closed by StopReactors
	work     context.Context    //nolint:containedctx // parent of every handler's context
	cancel   context.CancelFunc // cancels handlers still running when the stop budget ends
	consumes []jetstream.ConsumeContext
	inflight sync.WaitGroup
}

// reactor is one declared reactor with its NATS names.
type reactor struct {
	event    string // <service>.<Event>
	consumer string
	source   string // stream of the event's service
	subject  string
	durable  string
	dead     string // dead-letter subject
	handler  env.Handler
	pos      *position
}

func (b *Broker) reactorOf(r env.Reactor) (reactor, error) {
	service, name, err := SplitEvent(r.Event)
	if err != nil {
		return reactor{}, fmt.Errorf("reactor %s: %w", r.Consumer, err)
	}

	return reactor{
		event: r.Event, consumer: r.Consumer, source: service,
		subject: Subject(service, name), durable: Durable(b.p.Service, r.Consumer),
		dead: DLQSubject(b.p.Service, r.Consumer), handler: r.Handler, pos: newPosition(),
	}, nil
}

// fields of the reactor's log lines, then extra. Loggers are not derived
// with With: it races with concurrent writes in xlog's JSON core.
func (re reactor) fields(extra ...xlog.Field) []xlog.Field {
	return append([]xlog.Field{xlog.String("reactor", re.consumer), xlog.String("event", re.event)}, extra...)
}

// StartReactors starts a durable consumer per declared reactor on g.
func (b *Broker) StartReactors(ctx context.Context, g node.Group) error {
	if b.p.Env == nil {
		return nil
	}

	list := make([]reactor, 0, len(b.p.Env.Reactors()))

	for _, r := range b.p.Env.Reactors() {
		re, err := b.reactorOf(r)
		if err != nil {
			return err
		}

		list = append(list, re)
	}

	st := &b.reactors
	st.mu.Lock()
	st.stop = make(chan struct{})
	st.work, st.cancel = context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // StopReactors cancels
	st.mu.Unlock()

	for _, re := range list {
		g.Go(func(ctx context.Context) error { return b.run(ctx, re) })
	}

	return nil
}

// run ensures the reactor's streams and consumer, retrying while NATS is
// unreachable, then consumes until ctx ends. A consumer deleted on the
// server is recreated at the reactor's position and consumption resumes.
func (b *Broker) run(ctx context.Context, re reactor) error {
	var resume uint64 // 0: the consumer's own position

	for {
		var (
			cc   jetstream.ConsumeContext
			lost <-chan error
		)

		err := backoff.Retry(ctx, b.t.retry, func(ctx context.Context) error {
			c, l, err := b.consume(ctx, re, resume)
			cc, lost = c, l

			return err
		}, func(err error, in time.Duration) {
			b.log.Warn("reactor not started, retrying", re.fields(xlog.Err(err), xlog.Duration("retry_in", in))...)
		})
		if err != nil {
			return nil //nolint:nilerr // only the node's stop ends the retries
		}

		if !b.track(cc) {
			return nil
		}

		b.log.Info("reactor consuming", re.fields(xlog.String("durable", re.durable))...)

		gone := b.watch(ctx, re, lost)

		cc.Stop()
		b.untrack(cc)

		if !gone {
			return nil
		}

		resume = re.pos.resume()
		b.log.Warn("reactor consumer deleted on the server, recreating",
			re.fields(xlog.String("durable", re.durable), xlog.Uint64("resume_seq", resume))...)
	}
}

// watch waits until ctx ends (false) or the consumer is gone from the
// server (true). A missed heartbeat alone may be a network blip: the
// consumer is looked up before it counts as gone.
func (b *Broker) watch(ctx context.Context, re reactor, lost <-chan error) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err := <-lost:
			if !errors.Is(err, jetstream.ErrNoHeartbeat) || b.missing(ctx, re) {
				return true
			}
		}
	}
}

// missing reports whether the reactor's consumer is surely not on the
// server.
func (b *Broker) missing(ctx context.Context, re reactor) bool {
	jet, err := b.connected()
	if err != nil {
		return false
	}

	lctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
	defer cancel()

	_, err = jet.Consumer(lctx, StreamName(re.source), re.durable)

	return errors.Is(err, jetstream.ErrConsumerNotFound)
}

// consume ensures the source and dead-letter streams and the durable
// consumer, and starts consuming. resume > 0 starts a consumer that has to
// be created at that stream sequence. lost delivers the consume errors that
// may mean the consumer is gone.
//
//nolint:ireturn // ConsumeContext is only an interface
func (b *Broker) consume(
	ctx context.Context, re reactor, resume uint64,
) (jetstream.ConsumeContext, <-chan error, error) {
	jet, err := b.connected()
	if err != nil {
		return nil, nil, err
	}

	if err := ensureExists(ctx, jet, eventStream(re.source, bySubscribe)); err != nil {
		return nil, nil, err
	}

	if err := ensureExists(ctx, jet, deadStream(b.p.Service)); err != nil {
		return nil, nil, err
	}

	cfg, err := b.startAt(ctx, jet, re, resume)
	if err != nil {
		return nil, nil, err
	}

	cons, err := jet.CreateOrUpdateConsumer(ctx, StreamName(re.source), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("broker: consumer %s: %w", re.durable, err)
	}

	re.pos.started(cons.CachedInfo().AckFloor.Stream)

	sem := make(chan struct{}, b.t.concurrency)
	lost := make(chan error, 1)

	cc, err := cons.Consume(func(msg jetstream.Msg) { b.dispatch(re, sem, msg) },
		jetstream.PullMaxMessages(b.t.concurrency),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			b.log.Debug("reactor consume", re.fields(xlog.Err(err))...)

			if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) ||
				errors.Is(err, jetstream.ErrNoHeartbeat) {
				select {
				case lost <- err:
				default:
				}
			}
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("broker: consume %s: %w", re.durable, err)
	}

	return cc, lost, nil
}

// startAt is the consumer configuration with its start position. The
// server does not let an existing consumer's start change, so an existing
// consumer keeps its own; a missing one starts at resume when set, else at
// new messages.
func (b *Broker) startAt(
	ctx context.Context, jet jetstream.JetStream, re reactor, resume uint64,
) (jetstream.ConsumerConfig, error) {
	cfg := b.consumerConfig(re)

	cons, err := jet.Consumer(ctx, StreamName(re.source), re.durable)

	switch {
	case err == nil:
		have := cons.CachedInfo().Config
		cfg.DeliverPolicy, cfg.OptStartSeq, cfg.OptStartTime = have.DeliverPolicy, have.OptStartSeq, have.OptStartTime
	case !errors.Is(err, jetstream.ErrConsumerNotFound):
		return cfg, fmt.Errorf("broker: consumer %s: %w", re.durable, err)
	case resume > 0:
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, resume
	}

	return cfg, nil
}

// consumerConfig: explicit acks and bounded deliveries. A failed handler
// is redelivered after a delay growing with the delivery count (NakWithDelay,
// see handle); a message never acknowledged (the process died) comes back
// after AckWait. The consumer's own BackOff is not used: the server measures
// a NAK delay against AckWait, so with BackOff the delays would drift.
func (b *Broker) consumerConfig(re reactor) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       re.durable,
		Description:   "backplane reactor " + re.consumer + " of " + b.p.Service,
		FilterSubject: re.subject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       b.t.handlerTimeout + ackMargin,
		MaxDeliver:    b.t.maxDeliver,
		Metadata:      map[string]string{metaService: b.p.Service, "bp.consumer": re.consumer, "bp.event": re.event},
	}
}

// track records a started consume; false (and stopped) when the reactors
// are already stopping.
func (b *Broker) track(cc jetstream.ConsumeContext) bool {
	st := &b.reactors
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.stopping {
		cc.Stop()

		return false
	}

	st.consumes = append(st.consumes, cc)

	return true
}

// untrack forgets a stopped consume.
func (b *Broker) untrack(cc jetstream.ConsumeContext) {
	st := &b.reactors
	st.mu.Lock()
	defer st.mu.Unlock()

	for i, c := range st.consumes {
		if c == cc {
			st.consumes = append(st.consumes[:i], st.consumes[i+1:]...)

			return
		}
	}
}

// dispatch runs the handler of one message on its own goroutine, at most
// b.t.concurrency at once per reactor. JetStream calls it sequentially, so
// a full semaphore holds back the next message.
func (b *Broker) dispatch(re reactor, sem chan struct{}, msg jetstream.Msg) {
	st := &b.reactors

	select {
	case sem <- struct{}{}:
	case <-st.stop:
		_ = msg.Nak() // redelivered after the restart either way

		return
	}

	st.mu.Lock()
	if st.stopping {
		st.mu.Unlock()
		<-sem

		_ = msg.Nak() // redelivered after the restart either way

		return
	}

	st.inflight.Add(1)
	st.mu.Unlock()

	go func() {
		defer st.inflight.Done()
		defer func() { <-sem }()

		b.handle(st.work, re, msg)
	}()
}

// handle runs the handler in the trace of the event and settles the
// message: ack, nak with a delay, or dead-letter after the last delivery.
func (b *Broker) handle(work context.Context, re reactor, msg jetstream.Msg) {
	delivered, seq := uint64(1), uint64(0)
	if md, err := msg.Metadata(); err == nil {
		delivered, seq = md.NumDelivered, md.Sequence.Stream
		re.pos.delivered(seq)
	}

	ctx := otel.GetTextMapPropagator().Extract(work, carrier(msg.Headers()))

	hctx, cancel := context.WithTimeout(ctx, b.t.handlerTimeout)
	err := xtrace.Run(hctx, otel.Tracer(instrumentation), "process "+re.event,
		func(ctx context.Context, _ trace.Span) error { return call(ctx, re.handler, msg.Data()) },
		xtrace.WithSpanOptions(trace.WithSpanKind(trace.SpanKindConsumer)), xtrace.WithAttrs(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject()),
			attribute.String("messaging.consumer.group.name", re.durable),
			attribute.Int64("messaging.message.delivery_count", int64(delivered)),
		))

	cancel()

	log := b.log.Ctx()

	switch {
	case err == nil:
		if err := msg.Ack(); err != nil {
			log.Warn(ctx, "reactor ack", re.fields(xlog.Err(err))...)

			return
		}

		re.pos.settled(seq)
	case delivered < uint64(b.t.maxDeliver): //nolint:gosec // positive
		log.Warn(ctx, "reactor failed, redelivering", re.fields(xlog.Err(err), xlog.Uint64("delivered", delivered))...)

		if err := msg.NakWithDelay(nakDelay(b.t.nak, delivered)); err != nil {
			log.Warn(ctx, "reactor nak", re.fields(xlog.Err(err))...)
		}
	default:
		b.deadLetter(ctx, re, msg, err, delivered)
		re.pos.settled(seq)
	}
}

// deadLetter publishes the message to the reactor's dead-letter subject and
// terminates it.
func (b *Broker) deadLetter(ctx context.Context, re reactor, msg jetstream.Msg, cause error, n uint64) {
	id := re.durable + ":" + msg.Subject()
	if md, err := msg.Metadata(); err == nil {
		id = re.durable + ":" + md.Stream + ":" + strconv.FormatUint(md.Sequence.Stream, 10)
	}

	dead := &nats.Msg{
		Subject: re.dead, Data: msg.Data(), Header: deadHeaders(msg.Headers(), id, re.consumer, cause.Error(), n),
	}

	pctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
	defer cancel()

	jet, err := b.connected()
	if err == nil {
		_, err = jet.PublishMsg(pctx, dead)
	}

	log := b.log.Ctx()

	if err != nil {
		// The server will not deliver it again: the event stays in its
		// stream only.
		log.Error(ctx, "reactor gave up, dead letter not published", re.fields(
			xlog.Err(err), xlog.String("cause", cause.Error()), xlog.String("dead_letter", id), xlog.Uint64("delivered", n))...)
	} else {
		log.Error(ctx, "reactor gave up, dead-lettered", re.fields(
			xlog.Err(cause), xlog.String("subject", re.dead), xlog.Uint64("delivered", n))...)
	}

	if err := msg.TermWithReason("dead-lettered"); err != nil {
		log.Warn(ctx, "reactor term", re.fields(xlog.Err(err))...)
	}
}

// call runs h; a panic is an error.
func call(ctx context.Context, h env.Handler, in []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v\n%s", ErrHandlerPanic, r, debug.Stack())
		}
	}()

	_, err = h(ctx, in)

	return err
}

// nakDelay after the n-th failed delivery: p.Min doubling up to p.Max.
func nakDelay(p backoff.Policy, n uint64) time.Duration {
	d := p.Min
	for i := uint64(1); i < n && d < p.Max; i++ {
		d *= 2
	}

	return min(d, p.Max)
}

// StopReactors stops consuming and waits for handlers in flight; when ctx
// ends first, their contexts are cancelled.
func (b *Broker) StopReactors(ctx context.Context) error {
	st := &b.reactors

	st.mu.Lock()
	if st.stopping || st.stop == nil {
		st.mu.Unlock()

		return nil
	}

	st.stopping = true
	close(st.stop)
	consumes := st.consumes
	st.consumes = nil
	st.mu.Unlock()

	for _, cc := range consumes {
		cc.Stop()
	}

	done := make(chan struct{})

	go func() {
		st.inflight.Wait()
		close(done)
	}()

	defer st.cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrStopTimeout, ctx.Err())
	}
}
