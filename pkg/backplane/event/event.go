// Package event declares the events a service publishes and the reactors it
// runs. Events travel over the service's transport (NATS JetStream with
// CloudEvents metadata, M2); without one Publish returns ErrUnavailable.
// Reactors are recorded in the manifest as durable consumers.
package event

import (
	"context"
	"fmt"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
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

// PublishOption configures Publish.
type PublishOption interface{ apply(p *publishSettings) }

type publishSettings struct{ key string }

type key string

func (k key) apply(p *publishSettings) { p.key = string(k) }

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

// Publish sends the event.
func (r Ref[T]) Publish(ctx context.Context, v T, opts ...PublishOption) error {
	if r.env == nil {
		return fmt.Errorf("event: publish on an undeclared Ref: %w", ErrUnavailable)
	}

	var p publishSettings
	for _, o := range opts {
		o.apply(&p)
	}

	t := r.env.Transport()
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
func React[T any](scope deps.Scope, event string, fn func(ctx context.Context, v T) error) {
	e, n := decl.Env(scope, "reactor of "+event)

	consumer := event
	if n.Path() != "" {
		consumer = n.Path() + ":" + event
	}

	e.Manifest.Subscription(&backplanev1.Subscription{Event: event, Consumer: consumer})
	e.Reactor(consumer, func(ctx context.Context, in []byte) ([]byte, error) {
		var v T
		if err := decl.Decode(in, &v); err != nil {
			return nil, fmt.Errorf("reactor %s: decode: %w", consumer, err)
		}

		return nil, fn(ctx, v)
	})
}
