// Package backplane is the Go SDK. The author describes the service as one
// Config struct and one State built by an explicit constructor:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Postgres pgx.Config `json:"postgres"`
//	}
//
//	type State struct {
//	    DB    deps.Dependency[*pgxpool.Pool]
//	    Users *users.Repo
//	}
//
//	func NewState(root backplane.Root[Config]) (*State, error) { ... }
//
//	svc, err := backplane.Open(ctx, NewState)
//	svc.GRPC(svc.State().Users.Register)
//	err = svc.Run(ctx)
//
// Everything — the SDK's own parts and the author's tree — is one tree of
// nodes. Run starts it in order:
//
//	config → telemetry → health → platform port → author's tree → internal API → public ports → Consul → serving
//
// and stops it in exact reverse within one budget: readiness drops, Consul
// deregisters, listeners drain after a grace period, internal calls finish,
// then components stop before the dependencies they use. Consul, NATS,
// Temporal and the telemetry collector are optional: missing ones are
// warnings, never failures.
package backplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/gopherex/backplane/pkg/backplane/build"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
)

// Root is the root of the author's tree, handed to the State constructor:
// create components and dependencies under it, read the configuration from
// it. Embedding it in State is optional.
type Root[C any] struct {
	deps.Component

	cfg *C
	id  Identity
}

// Config is a copy of the loaded configuration. Static fields never change;
// config.Live fields are shared by every copy and update in place.
func (r Root[C]) Config() C {
	if r.cfg == nil {
		var zero C

		return zero
	}

	return *r.cfg
}

// Identity of the instance.
func (r Root[C]) Identity() Identity { return r.id }

// Service is one process on the SDK: the author's State plus what it serves.
type Service[St any] struct {
	*core

	state *St
}

// State is what the constructor built.
func (s *Service[St]) State() *St { return s.state }

// Open loads the configuration, builds the State with newState and returns a
// service ready to declare routes and Run. Nothing listens until Run.
//
// ctx bounds loading only (a required Live value may wait for Consul); the
// service does not depend on it afterwards. An invalid configuration or a
// failing constructor is an error; an absent Consul is not. Call Close if
// Run will not be called.
func Open[C config.Backplaner, St any](
	ctx context.Context, newState func(root Root[C]) (*St, error), opts ...Option,
) (*Service[St], error) {
	o := options{id: Identity{Service: build.Get().Service, Version: build.Get().Version}, slog: true}
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

	block := (*conf.Value()).BackplaneConfig()
	if err := validate(block); err != nil {
		return nil, errors.Join(fmt.Errorf("backplane: %w", err), conf.Close())
	}

	c := newCore(ctx, o, link.ConfigState(conf), block)

	scope := link.Scope(c.app).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
	root := Root[C]{Component: scope, cfg: conf.Value(), id: c.id}

	state, err := newState(root)
	if err != nil {
		return nil, fmt.Errorf("backplane: state: %w", errors.Join(err, c.Close()))
	}

	return &Service[St]{core: c, state: state}, nil
}

// ErrConfig: the SDK block is unusable.
var ErrConfig = errors.New("backplane: invalid configuration")

// validate checks what the schema cannot express.
func validate(b config.Backplane) error {
	switch {
	case b.Shutdown.Timeout <= 0:
		return fmt.Errorf("%w: shutdown.timeout must be positive, got %v", ErrConfig, b.Shutdown.Timeout)
	case b.Shutdown.Drain < 0 || b.Shutdown.Drain >= b.Shutdown.Timeout:
		return fmt.Errorf("%w: shutdown.drain must be in [0, timeout), got %v", ErrConfig, b.Shutdown.Drain)
	}

	return validateNATS(b.NATS)
}

// validateNATS checks the stream settings JetStream would reject.
func validateNATS(n config.NATS) error {
	switch {
	case !n.Enabled():
		return nil
	case n.MaxAge < 0:
		return fmt.Errorf("%w: nats.max_age must be >= 0 (0: unlimited), got %v", ErrConfig, n.MaxAge)
	case n.DLQMaxAge < 0:
		return fmt.Errorf("%w: nats.dlq_max_age must be >= 0 (0: unlimited), got %v", ErrConfig, n.DLQMaxAge)
	case n.DedupWindow <= 0 || (n.MaxAge > 0 && n.DedupWindow > n.MaxAge):
		return fmt.Errorf("%w: nats.dedup_window must be in (0, max_age], got %v", ErrConfig, n.DedupWindow)
	}

	return nil
}
