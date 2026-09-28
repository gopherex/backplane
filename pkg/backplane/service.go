// Package backplane is the Go SDK. The author describes the service as one
// Config struct and one State struct built by an explicit constructor:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Postgres pgx.Config `json:"postgres"`
//	}
//
//	type State struct {
//	    backplane.Root[Config]
//	    DB    deps.Dependency[*pgxpool.Pool]
//	    Users *users.Repo
//	}
//
//	func NewState(root backplane.Root[Config]) (*State, error) { ... }
//
//	svc, err := backplane.Open(ctx, NewState)
//	svc.GRPC(func(r grpc.ServiceRegistrar) { ... svc.State().Users ... })
//	err = svc.Run(ctx)
//
// Run brings everything up as ordered components on one lifecycle:
//
//	config → telemetry → health → platform port → node tree → public ports → Consul presence → serving gate
//
// and stops them in reverse within one time budget. Consul, NATS, Temporal
// and the telemetry collector are optional for a service: missing ones are
// warnings, never failures.
package backplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/gopherex/backplane/pkg/backplane/build"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// Owner is what hook, activity and event declarations are made on: the Root
// inside the constructor, the Service after Open.
type Owner interface {
	Name() string
	owner()
}

// Root is the root node of the service tree: embed it in State. It carries
// the configuration and the identity; components, dependencies and
// singletons are created under it.
type Root[C any] struct {
	deps.Component

	cfg *C
	id  Identity
}

// Config is the loaded configuration. The pointer is stable for the life of
// the service: static fields never change, config.Live fields update in
// place.
func (r Root[C]) Config() *C { return r.cfg }

// Identity of the instance.
func (r Root[C]) Identity() Identity { return r.id }

func (Root[C]) owner() {}

// Service is one process on the SDK: the author's State plus what it serves.
type Service[St any] struct {
	*core

	state *St
}

// State is what the constructor built.
func (s *Service[St]) State() *St { return s.state }

func (*Service[St]) owner() {}

// Open loads the configuration, builds the State with newState and returns a
// service ready to declare routes and Run. Nothing listens until Run. The
// name comes from build.Service unless Name is given.
//
// An invalid configuration or a failing constructor is an error; an absent
// Consul is not.
func Open[C config.Backplaner, St any](
	ctx context.Context, newState func(root Root[C]) (*St, error), opts ...Option,
) (*Service[St], error) {
	o := options{id: Identity{Service: build.Service, Version: build.Get().Version}}
	for _, opt := range opts {
		opt(&o)
	}

	if o.id.Service == "" {
		return nil, fmt.Errorf("backplane: %w (or pass backplane.Name())", build.ErrUnnamed)
	}

	conf, err := config.Open[C](ctx, append([]config.Option{config.Service(o.id.Service)}, o.config...)...)
	if err != nil {
		return nil, fmt.Errorf("backplane: %w", err)
	}

	c := newCore(ctx, o, conf, (*conf.Value()).BackplaneConfig())
	root := Root[C]{Component: deps.Adopt(c.root), cfg: conf.Value(), id: c.id}
	c.attach(root)

	state, err := newState(root)
	if err != nil {
		c.detach()

		return nil, fmt.Errorf("backplane: state: %w", errors.Join(err, conf.Close()))
	}

	svc := &Service[St]{core: c, state: state}
	c.attach(svc)

	return svc, nil
}
