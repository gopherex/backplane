package deps

import (
	"context"
	"fmt"
	"sync"

	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// Singleton is built on first Get and closed at stop if it was built. It is
// not part of readiness. Its factory must not create child nodes: it runs
// after the service started.
type Singleton[T any] struct {
	c *singletonCell[T]
}

type singletonCell[T any] struct {
	node    *node.Node
	factory Factory[T]

	mu     sync.Mutex
	value  T
	built  bool
	closed bool
}

// NewSingleton adds a singleton under parent.
func NewSingleton[T any](parent Scope, f Factory[T], opts ...SingletonOption) Singleton[T] {
	o := nodeOptions{name: f.Name()}
	for _, opt := range opts {
		opt.applySingleton(&o)
	}

	c := &singletonCell[T]{node: nodeOf(parent).Child(o.name, node.Singleton, false), factory: f}
	c.node.OnStop(c.close)

	return Singleton[T]{c: c}
}

// Get builds the value once; a failed build is retried by the next Get.
// After stop it returns ErrClosed.
func (s Singleton[T]) Get(ctx context.Context) (T, error) {
	var zero T
	if s.c == nil {
		return zero, ErrNotReady
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	switch {
	case s.c.closed:
		return zero, ErrClosed
	case s.c.built:
		return s.c.value, nil
	}

	value, err := s.c.factory.Provide(ctx, Component{n: s.c.node})
	if err != nil {
		return zero, fmt.Errorf("provide %s: %w", s.c.node.Path(), err)
	}

	s.c.value, s.c.built = value, true

	return value, nil
}

// IsZero reports whether s was not created by NewSingleton.
func (s Singleton[T]) IsZero() bool { return s.c == nil }

func (c *singletonCell[T]) close(ctx context.Context) error {
	var zero T

	c.mu.Lock()
	value, built := c.value, c.built
	c.value, c.built, c.closed = zero, false, true
	c.mu.Unlock()

	if !built {
		return nil
	}

	if err := c.factory.Close(ctx, value); err != nil {
		return fmt.Errorf("close %s: %w", c.node.Path(), err)
	}

	return nil
}
