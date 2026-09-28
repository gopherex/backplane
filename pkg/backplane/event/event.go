// Package event declares the events a service publishes and the reactors it
// runs. Events travel over the service's transport (NATS JetStream with
// CloudEvents metadata, M2); without one Publish returns ErrUnavailable.
// Reactors are recorded in the manifest as durable consumers.
package event

import (
	"context"
	"fmt"
	"time"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
)

// ErrUnavailable is returned while no transport carries events.
var ErrUnavailable = env.ErrUnavailable

// Ref is a declared event. The zero Ref is undeclared: Publish fails.
type Ref[T any] struct {
	env  *env.Env
	name string
}

// Option interfaces, one per function.
type (
	// PublishOption configures Publish.
	PublishOption interface{ applyPublish(p *publishSettings) }
	// ReactOption configures React.
	ReactOption interface{ applyReact(d *env.Delivery) }
)

type publishSettings struct{ key string }

type key string

func (k key) applyPublish(p *publishSettings) { p.key = string(k) }

// Key sets the CloudEvents subject (ordering key).
func Key(k string) PublishOption { return key(k) }

// Declare registers an event the service publishes. Declare it where it is
// raised: any component can.
func Declare[T any](scope deps.Scope, name string) Ref[T] {
	e, _ := decl.Env(scope, "event "+name)
	e.Manifest.Event(&backplanev1.Event{Name: name, Schema: decl.Schema[T](e.Service, "event_"+name)})

	return Ref[T]{env: e, name: name}
}

// Name is the full event name <service>.<Name>.
func (r Ref[T]) Name() string {
	if r.env == nil {
		return ""
	}

	return r.env.Service + "." + r.name
}

// Publish sends the event and returns once the transport stored it. It
// waits no longer than ctx allows; a ctx without a deadline gets the
// transport's own (5s on NATS).
func (r Ref[T]) Publish(ctx context.Context, v T, opts ...PublishOption) error {
	if r.env == nil {
		return fmt.Errorf("event: publish on an undeclared Ref: %w", ErrUnavailable)
	}

	var p publishSettings
	for _, o := range opts {
		o.applyPublish(&p)
	}

	t := r.env.Broker()
	if t == nil {
		return fmt.Errorf("event %s: %w", r.Name(), ErrUnavailable)
	}

	payload, err := decl.Encode(v)
	if err != nil {
		return fmt.Errorf("event %s: encode: %w", r.Name(), err)
	}

	if err := t.Publish(ctx, r.Name(), p.key, payload); err != nil {
		return fmt.Errorf("event %s: %w", r.Name(), err)
	}

	return nil
}

// React subscribes fn to another service's event by full name
// ("iam.UserRegistered"). The durable consumer is named after the scope's
// node path and the event, so each reactor keeps its own position.
//
// A handler that returns an error (or panics) gets the message again after
// a growing delay; when the last delivery fails too, the message goes to
// the service's dead letters. Options change the defaults: MaxDeliver 5,
// Concurrency 4, Timeout 30s, Redelivery 1s..1m, StartAt(StartNew). They
// panic on values that make no sense, at declaration.
func React[T any](scope deps.Scope, event string, fn func(ctx context.Context, v T) error, opts ...ReactOption) {
	e, n := decl.Env(scope, "reactor of "+event)

	var d env.Delivery
	for _, o := range opts {
		o.applyReact(&d)
	}

	consumer := event
	if n.Path() != "" {
		consumer = n.Path() + ":" + event
	}

	e.Manifest.Subscription(&backplanev1.Subscription{Event: event, Consumer: consumer})

	handler := func(ctx context.Context, in []byte) ([]byte, error) {
		var v T
		if err := decl.Decode(in, &v); err != nil {
			return nil, fmt.Errorf("reactor %s: decode: %w", consumer, err)
		}

		return nil, fn(ctx, v)
	}

	e.Reactor(env.Reactor{Event: event, Consumer: consumer, Handler: handler, Delivery: d})
}

type maxDeliver int

func (m maxDeliver) applyReact(d *env.Delivery) { d.MaxDeliver = int(m) }

// MaxDeliver bounds the deliveries of one message, the first included;
// after the n-th failure it is dead-lettered. MaxDeliver(1) never retries.
// It panics unless n >= 1.
func MaxDeliver(n int) ReactOption {
	if n < 1 {
		panic(fmt.Sprintf("event: MaxDeliver must be >= 1, got %d", n))
	}

	return maxDeliver(n)
}

type concurrency int

func (c concurrency) applyReact(d *env.Delivery) { d.Concurrency = int(c) }

// Concurrency bounds the handler calls in flight at once, per instance.
// Concurrency(1) runs them one at a time; a failed message still comes
// back after its delay, behind the ones that followed it. It panics unless
// n >= 1.
func Concurrency(n int) ReactOption {
	if n < 1 {
		panic(fmt.Sprintf("event: Concurrency must be >= 1, got %d", n))
	}

	return concurrency(n)
}

type timeout time.Duration

func (t timeout) applyReact(d *env.Delivery) { d.Timeout = time.Duration(t) }

// Timeout of one handler call: its ctx is cancelled after d and the call
// counts as failed. A message nobody settled (the instance died) comes back
// after d plus a margin. It panics unless d > 0.
func Timeout(d time.Duration) ReactOption {
	if d <= 0 {
		panic(fmt.Sprintf("event: Timeout must be positive, got %v", d))
	}

	return timeout(d)
}

type redelivery backoff.Policy

func (r redelivery) applyReact(d *env.Delivery) { d.Redelivery = backoff.Policy(r) }

// Redelivery bounds the delay before a failed message comes back: minDelay
// after the first failure, doubling with every delivery up to maxDelay. It
// panics unless 0 < minDelay <= maxDelay.
func Redelivery(minDelay, maxDelay time.Duration) ReactOption {
	p := backoff.Policy{Min: minDelay, Max: maxDelay}
	if err := p.Validate(); err != nil {
		panic("event: redelivery: " + err.Error())
	}

	return redelivery(p)
}

// Start is where a reactor's consumer starts when it is created.
type Start int

// Starts of StartAt.
const (
	// StartNew: events published after the consumer is created (default).
	StartNew Start = iota
	// StartAll: every event the source stream still retains.
	StartAll
)

type startAt Start

func (s startAt) applyReact(d *env.Delivery) { d.StartAll = Start(s) == StartAll }

// StartAt chooses where the reactor's consumer starts when it is first
// created. It applies at creation only: an existing consumer keeps its
// position, so adding StartAt(StartAll) to a running reactor replays
// nothing. It panics on an unknown Start.
func StartAt(s Start) ReactOption {
	if s != StartNew && s != StartAll {
		panic(fmt.Sprintf("event: unknown Start %d", s))
	}

	return startAt(s)
}
