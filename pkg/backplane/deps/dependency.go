package deps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// Errors.
var (
	// ErrNotReady: the dependency has not been provided (yet).
	ErrNotReady = errors.New("deps: not ready")
	// ErrClosed: the node has stopped.
	ErrClosed = errors.New("deps: closed")
)

const (
	defaultMinDelay = time.Second
	defaultMaxDelay = 30 * time.Second
	lateCloseBudget = 5 * time.Second
)

// Dependency is a required external resource: provided when its node
// starts (the start retries until it succeeds), part of readiness through
// its probe, closed at stop. Public and internal traffic starts only after
// every required dependency is ready and stops before they close, so Get
// always holds the value where handlers run.
type Dependency[T any] struct {
	c *cell[T]
}

// Optional is a dependency the service works without: provided in the
// background with retries, never part of readiness. Get reports whether it
// is there right now.
type Optional[T any] struct {
	c *cell[T]
}

type cell[T any] struct {
	node     *node.Node
	provider Provider[T]
	policy   backoff.Policy

	mu     sync.RWMutex
	value  T
	ready  bool
	closed bool
	err    error
}

// Option configures NewDependency and NewOptional.
type Option interface{ applyDependency(o *nodeOptions) }

// SingletonOption configures NewSingleton.
type SingletonOption interface{ applySingleton(o *nodeOptions) }

type nodeOptions struct {
	name   string
	policy backoff.Policy
}

type nameOption string

func (n nameOption) applyDependency(o *nodeOptions) { o.name = string(n) }
func (n nameOption) applySingleton(o *nodeOptions)  { o.name = string(n) }

// NameOption applies to dependencies and singletons.
type NameOption interface {
	Option
	SingletonOption
}

// Name overrides the provider's name: two databases of one kind.
func Name(name string) NameOption { return nameOption(name) }

type backoffOption backoff.Policy

func (b backoffOption) applyDependency(o *nodeOptions) { o.policy = backoff.Policy(b) }

// Backoff bounds the delay between provide attempts (default 1s..30s,
// jittered). It panics unless 0 < minDelay <= maxDelay.
func Backoff(minDelay, maxDelay time.Duration) Option {
	p := backoff.Policy{Min: minDelay, Max: maxDelay}
	if err := p.Validate(); err != nil {
		panic("deps: " + err.Error())
	}

	return backoffOption(p)
}

func newCell[T any](parent Scope, p Provider[T], optional bool, opts []Option) *cell[T] {
	o := nodeOptions{name: p.Name(), policy: backoff.Policy{Min: defaultMinDelay, Max: defaultMaxDelay}}
	for _, opt := range opts {
		opt.applyDependency(&o)
	}

	c := &cell[T]{
		node:     nodeOf(parent).Child(o.name, node.Dependency, optional),
		provider: p, policy: o.policy, err: ErrNotReady,
	}
	c.node.OnStop(c.close)

	return c
}

// NewDependency adds a required dependency under parent: the start waits
// for it, retrying with backoff.
func NewDependency[T any](parent Scope, p Provider[T], opts ...Option) Dependency[T] {
	c := newCell(parent, p, false, opts)
	c.node.OnStart(func(ctx context.Context) error {
		return backoff.Retry(ctx, c.policy, c.provide, c.warn)
	})
	c.node.Ready(probe.FromError(c.probe))

	return Dependency[T]{c: c}
}

// NewOptional adds an optional dependency under parent: the start does not
// wait; providing retries in the background until it succeeds or the
// service stops.
func NewOptional[T any](parent Scope, p Provider[T], opts ...Option) Optional[T] {
	c := newCell(parent, p, true, opts)
	c.node.Go(func(ctx context.Context) error {
		_ = backoff.Retry(ctx, c.policy, c.provide, c.warn)

		return nil
	})

	return Optional[T]{c: c}
}

// Static is a dependency that is always ready with v: tests, and values
// the service owns itself.
func Static[T any](v T) Dependency[T] {
	return Dependency[T]{c: &cell[T]{value: v, ready: true}}
}

// StaticOptional is an optional dependency that is present with v; the
// zero Optional is one that is absent.
func StaticOptional[T any](v T) Optional[T] {
	return Optional[T]{c: &cell[T]{value: v, ready: true}}
}

// Get returns the value. It is set wherever handlers run: they are served
// only while every required dependency is ready.
func (d Dependency[T]) Get() T {
	v, _ := d.c.get()

	return v
}

// Ready reports whether the value is provided.
func (d Dependency[T]) Ready() bool { return d.c.isReady() }

// Err is why the dependency is not ready; nil when it is.
func (d Dependency[T]) Err() error { return d.c.lastErr() }

// IsZero reports whether d was not created by NewDependency or Static.
func (d Dependency[T]) IsZero() bool { return d.c == nil }

// Get returns the value and whether it is there right now.
func (o Optional[T]) Get() (T, bool) { return o.c.get() }

// Err is why the dependency is absent; nil when it is there.
func (o Optional[T]) Err() error { return o.c.lastErr() }

// IsZero reports whether o was not created by NewOptional or
// StaticOptional (an absent dependency).
func (o Optional[T]) IsZero() bool { return o.c == nil }

func (c *cell[T]) get() (T, bool) {
	var zero T
	if c == nil {
		return zero, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.ready {
		return zero, false
	}

	return c.value, true
}

func (c *cell[T]) isReady() bool {
	_, ok := c.get()

	return ok
}

func (c *cell[T]) lastErr() error {
	if c == nil {
		return ErrNotReady
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.err
}

func (c *cell[T]) warn(err error, in time.Duration) {
	c.node.Log().Warn("dependency unavailable, retrying", xlog.Err(err), xlog.Duration("in", in))
}

func (c *cell[T]) provide(ctx context.Context) error {
	value, err := c.provider.Provide(ctx, Component{n: c.node})

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		if err == nil {
			c.closeLate(ctx, value)
		}

		return ErrClosed
	}

	defer c.mu.Unlock()

	if err != nil {
		c.err = fmt.Errorf("provide %s: %w", c.node.Path(), err)

		return c.err
	}

	c.value, c.ready, c.err = value, true, nil
	c.node.Log().Info("dependency ready")

	return nil
}

// closeLate releases a value provided after the node closed.
func (c *cell[T]) closeLate(ctx context.Context, value T) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lateCloseBudget)
	defer cancel()

	if err := c.provider.Close(ctx, value); err != nil {
		c.node.Log().Warn("close after stop", xlog.Err(err))
	}
}

func (c *cell[T]) probe(ctx context.Context) error {
	value, ready := c.get()
	if !ready {
		return c.lastErr()
	}

	if err := c.provider.Probe(ctx, value); err != nil {
		return fmt.Errorf("probe %s: %w", c.node.Path(), err)
	}

	return nil
}

func (c *cell[T]) close(ctx context.Context) error {
	var zero T

	c.mu.Lock()
	value, ready := c.value, c.ready
	c.value, c.ready, c.closed, c.err = zero, false, true, ErrClosed
	c.mu.Unlock()

	if !ready {
		return nil
	}

	if err := c.provider.Close(ctx, value); err != nil {
		return fmt.Errorf("close %s: %w", c.node.Path(), err)
	}

	return nil
}
