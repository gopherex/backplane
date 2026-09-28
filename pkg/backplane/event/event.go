// Package event declares the events a service publishes and the reactors it
// runs. Transport is NATS JetStream with CloudEvents metadata (M2); until then
// Publish returns ErrUnavailable and React only records the subscription.
package event

import (
	"context"
	"errors"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
)

// ErrUnavailable is returned while no broker is configured.
var ErrUnavailable = errors.New("event: broker unavailable")

// Ref is a declared event.
type Ref[T any] struct {
	svc  backplane.Owner
	name string
}

// PublishOption configures Publish.
type PublishOption func(*publishSettings)

type publishSettings struct{ key string }

// Key sets the CloudEvents subject (ordering key).
func Key(k string) PublishOption { return func(p *publishSettings) { p.key = k } }

// Declare registers an event in the manifest, on the Root inside the State
// constructor or on the Service after Open.
func Declare[T any](svc backplane.Owner, name string) *Ref[T] {
	e := &backplanev1.Event{Name: name, Schema: decl.Schema[T](svc.Name(), "event_"+name)}
	decl.To(svc).Event(e)

	return &Ref[T]{svc: svc, name: name}
}

// Name is the full event name <service>.<Name>.
func (r *Ref[T]) Name() string { return r.svc.Name() + "." + r.name }

// Publish sends the event.
//
// Not wired until M2 (NATS JetStream): always returns ErrUnavailable.
func (r *Ref[T]) Publish(context.Context, T, ...PublishOption) error {
	return ErrUnavailable
}

// React subscribes fn to an event by full name ("iam.UserRegistered").
//
// Not wired until M2, when it becomes a durable JetStream consumer.
func React[T any](_ backplane.Owner, _ string, fn func(context.Context, T) error) {
	_ = fn
}
