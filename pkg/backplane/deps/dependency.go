package deps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane/internal/tree"
)

// ErrNotReady: the dependency has not been provided (yet).
var ErrNotReady = errors.New("deps: not ready")

const (
	defaultRetryFirst = time.Second
	defaultRetryLimit = 30 * time.Second
	retryFactor       = 2
)

// Dependency is an external resource: provided when the tree starts, part
// of readiness through its probe (unless optional), closed at stop.
type Dependency[T any] struct {
	c *depCell[T]
}

type depCell[T any] struct {
	node     *tree.Node
	provider Provider[T]
	retryMin time.Duration
	retryMax time.Duration

	mu    sync.RWMutex
	value T
	ready bool
	err   error
}

// DependencyOption configures NewDependency.
type DependencyOption interface{ applyDependency(o *nodeOptions) }

// SingletonOption configures NewSingleton.
type SingletonOption interface{ applySingleton(o *nodeOptions) }

type nodeOptions struct {
	name     string
	optional bool
	retryMin time.Duration
	retryMax time.Duration
}

type nameOption string

func (n nameOption) applyDependency(o *nodeOptions) { o.name = string(n) }
func (n nameOption) applySingleton(o *nodeOptions)  { o.name = string(n) }

// NameOption applies to dependencies and singletons.
type NameOption interface {
	DependencyOption
	SingletonOption
}

// Name overrides the provider's name: two databases of one kind.
func Name(name string) NameOption { return nameOption(name) }

type optionalOption struct{}

func (optionalOption) applyDependency(o *nodeOptions) { o.optional = true }

// Optional: failing to provide is a warning, readiness does not wait for
// it, providing keeps retrying in the background.
func Optional() DependencyOption { return optionalOption{} }

type retryOption struct{ lo, hi time.Duration }

func (r retryOption) applyDependency(o *nodeOptions) { o.retryMin, o.retryMax = r.lo, r.hi }

// Retry sets the backoff between provide attempts (default 1s..30s).
func Retry(initial, maxDelay time.Duration) DependencyOption { return retryOption{initial, maxDelay} }

// NewDependency adds a dependency under parent.
func NewDependency[T any](parent Scope, p Provider[T], opts ...DependencyOption) Dependency[T] {
	o := nodeOptions{name: p.Name(), retryMin: defaultRetryFirst, retryMax: defaultRetryLimit}
	for _, opt := range opts {
		opt.applyDependency(&o)
	}

	c := &depCell[T]{
		node:     parent.node().Child(o.name, tree.Dependency, o.optional),
		provider: p, retryMin: o.retryMin, retryMax: o.retryMax,
		err: ErrNotReady,
	}

	if o.optional {
		// The service starts without it; retries run in the background and
		// end quietly at stop.
		c.node.OnStart(func(ctx context.Context) error {
			if err := c.provide(ctx); err != nil {
				c.node.Go(func(ctx context.Context) error {
					_ = c.retry(ctx, err)

					return nil
				})
			}

			return nil
		})
	} else {
		c.node.OnStart(func(ctx context.Context) error { return c.retry(ctx, c.provide(ctx)) })
		c.node.Ready(probe.FromError(c.probe))
	}

	c.node.OnStop(c.close)

	return Dependency[T]{c: c}
}

// Get returns the value; the zero value until the dependency is ready.
func (d Dependency[T]) Get() T {
	if d.c == nil {
		var zero T

		return zero
	}

	d.c.mu.RLock()
	defer d.c.mu.RUnlock()

	return d.c.value
}

// Ready reports whether the value has been provided.
func (d Dependency[T]) Ready() bool {
	if d.c == nil {
		return false
	}

	d.c.mu.RLock()
	defer d.c.mu.RUnlock()

	return d.c.ready
}

// Err is why the dependency is not ready; nil when ready.
func (d Dependency[T]) Err() error {
	if d.c == nil {
		return ErrNotReady
	}

	d.c.mu.RLock()
	defer d.c.mu.RUnlock()

	return d.c.err
}

func (c *depCell[T]) provide(ctx context.Context) error {
	value, err := c.provider.Provide(ctx, Adopt(c.node))

	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.err = fmt.Errorf("provide %s: %w", c.node.Path(), err)

		return c.err
	}

	c.value, c.ready, c.err = value, true, nil
	c.node.Log().Info("dependency ready")

	return nil
}

// retry provides with backoff while err is set, until success or ctx ends.
func (c *depCell[T]) retry(ctx context.Context, err error) error {
	for delay := c.retryMin; err != nil; delay = min(delay*retryFactor, c.retryMax) {
		c.node.Log().Warn("dependency unavailable, retrying", xlog.Err(err), xlog.Duration("in", delay))

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", c.node.Path(), ctx.Err())
		case <-time.After(delay):
		}

		err = c.provide(ctx)
	}

	return nil
}

func (c *depCell[T]) probe(ctx context.Context) error {
	c.mu.RLock()
	value, ready, err := c.value, c.ready, c.err
	c.mu.RUnlock()

	if !ready {
		return err
	}

	if err := c.provider.Probe(ctx, value); err != nil {
		return fmt.Errorf("probe %s: %w", c.node.Path(), err)
	}

	return nil
}

func (c *depCell[T]) close(ctx context.Context) error {
	c.mu.Lock()
	value, ready := c.value, c.ready
	c.ready, c.err = false, ErrNotReady
	c.mu.Unlock()

	if !ready {
		return nil
	}

	if err := c.provider.Close(ctx, value); err != nil {
		return fmt.Errorf("close %s: %w", c.node.Path(), err)
	}

	return nil
}
