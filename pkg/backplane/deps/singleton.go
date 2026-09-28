package deps

import (
	"context"
	"fmt"
	"sync"

	"github.com/gopherex/backplane/pkg/backplane/internal/tree"
)

// Singleton is built on first Get and closed at stop if it was built. It is
// not part of readiness.
type Singleton[T any] struct {
	c *singletonCell[T]
}

type singletonCell[T any] struct {
	node    *tree.Node
	factory Factory[T]

	mu    sync.Mutex
	value T
	built bool
}

// NewSingleton adds a singleton under parent.
func NewSingleton[T any](parent Scope, f Factory[T], opts ...SingletonOption) Singleton[T] {
	o := nodeOptions{name: f.Name()}
	for _, opt := range opts {
		opt.applySingleton(&o)
	}

	c := &singletonCell[T]{node: parent.node().Child(o.name, tree.Singleton, false), factory: f}
	c.node.OnStop(c.close)

	return Singleton[T]{c: c}
}

// Get builds the value once; a failed build is retried by the next Get.
func (s Singleton[T]) Get(ctx context.Context) (T, error) {
	var zero T
	if s.c == nil {
		return zero, ErrNotReady
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	if s.c.built {
		return s.c.value, nil
	}

	value, err := s.c.factory.Provide(ctx, Adopt(s.c.node))
	if err != nil {
		return zero, fmt.Errorf("provide %s: %w", s.c.node.Path(), err)
	}

	s.c.value, s.c.built = value, true

	return value, nil
}

func (c *singletonCell[T]) close(ctx context.Context) error {
	c.mu.Lock()
	value, built := c.value, c.built
	c.built = false
	c.mu.Unlock()

	if !built {
		return nil
	}

	if err := c.factory.Close(ctx, value); err != nil {
		return fmt.Errorf("close %s: %w", c.node.Path(), err)
	}

	return nil
}
