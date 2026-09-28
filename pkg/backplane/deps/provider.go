package deps

import (
	"context"
	"reflect"
	"strings"
	"unicode"
)

// Provider builds and serves a dependency. Contrib packages implement it
// once per library (config section, instrumentation, ping, close).
type Provider[T any] interface {
	// Name is the node name, e.g. "postgres".
	Name() string
	Provide(ctx context.Context, s Scope) (T, error)
	// Probe is part of readiness while the dependency is required.
	Probe(ctx context.Context, v T) error
	Close(ctx context.Context, v T) error
}

// Factory builds a singleton: a Provider without a probe.
type Factory[T any] interface {
	Name() string
	Provide(ctx context.Context, s Scope) (T, error)
	Close(ctx context.Context, v T) error
}

// FuncOption configures Func.
type FuncOption[T any] func(*funcProvider[T])

// WithProbe sets the readiness probe.
func WithProbe[T any](probe func(ctx context.Context, v T) error) FuncOption[T] {
	return func(f *funcProvider[T]) { f.probe = probe }
}

// WithClose sets how the value is released.
func WithClose[T any](closeFn func(ctx context.Context, v T) error) FuncOption[T] {
	return func(f *funcProvider[T]) { f.close = closeFn }
}

// Func adapts plain functions into a Provider (and so a Factory). The node
// is named after T ("pool" for *pgxpool.Pool); rename it with deps.Name.
func Func[T any](provide func(ctx context.Context) (T, error), opts ...FuncOption[T]) Provider[T] {
	f := funcProvider[T]{name: typeName[T](), provide: provide}
	for _, o := range opts {
		o(&f)
	}

	return f
}

type funcProvider[T any] struct {
	name    string
	provide func(ctx context.Context) (T, error)
	probe   func(ctx context.Context, v T) error
	close   func(ctx context.Context, v T) error
}

func (f funcProvider[T]) Name() string { return f.name }

func (f funcProvider[T]) Provide(ctx context.Context, _ Scope) (T, error) { return f.provide(ctx) }

func (f funcProvider[T]) Probe(ctx context.Context, v T) error {
	if f.probe == nil {
		return nil
	}

	return f.probe(ctx, v)
}

func (f funcProvider[T]) Close(ctx context.Context, v T) error {
	if f.close == nil {
		return nil
	}

	return f.close(ctx, v)
}

// typeName: "*pgxpool.Pool" -> "pool".
func typeName[T any]() string {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	name := t.Name()
	if name == "" {
		name = t.Kind().String()
	}

	name, _, _ = strings.Cut(name, "[")
	runes := []rune(name)
	runes[0] = unicode.ToLower(runes[0])

	return string(runes)
}
