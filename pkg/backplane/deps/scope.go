// Package deps is how a service is composed: a tree of components,
// dependencies and singletons, built explicitly by the author's constructor.
// Every node is a Scope with its own name, logger and instrumentation; nodes
// start in creation order and stop in reverse.
//
// It does not depend on the backplane package, so contrib providers need only
// this one.
package deps

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	"github.com/gopherex/backplane/pkg/backplane/internal/tree"
)

// Scope is a node of the service tree.
type Scope interface {
	// Name of the node; Path is its place in the tree ("users/templates").
	Name() string
	Path() string
	// Log carries node=<path>.
	Log() *xlog.Logger
	Tracer() trace.Tracer
	Meter() metric.Meter
	// Span runs fn in a span of this node's tracer.
	Span(ctx context.Context, name string, fn func(ctx context.Context) error, opts ...xtrace.SpanOption) error
	// Go runs fn under the service lifecycle once the tree has started; its
	// error stops the service.
	Go(fn func(ctx context.Context) error)
	// OnStart and OnStop hook the node into the lifecycle.
	OnStart(fn func(ctx context.Context) error)
	OnStop(fn func(ctx context.Context) error)

	node() *tree.Node
}

// Component is a named node of the author's code. Embed it:
//
//	type UserRepo struct {
//	    deps.Component
//	    pool deps.Dependency[*pgxpool.Pool]
//	}
type Component struct {
	n *tree.Node
}

// NewComponent creates a component under parent.
func NewComponent(parent Scope, name string) Component {
	return Component{n: parent.node().Child(name, tree.Component, false)}
}

// Adopt wraps a tree node; the SDK uses it for the root.
func Adopt(n *tree.Node) Component { return Component{n: n} }

// Name of the component.
func (c Component) Name() string { return c.n.Name() }

// Path of the component in the tree.
func (c Component) Path() string { return c.n.Path() }

// Log is the component's logger.
func (c Component) Log() *xlog.Logger { return c.n.Log() }

// Tracer of the component.
func (c Component) Tracer() trace.Tracer { return c.n.Scope().Tracer() }

// Meter of the component.
func (c Component) Meter() metric.Meter { return c.n.Scope().Meter() }

// Span runs fn in a span named name.
func (c Component) Span(
	ctx context.Context, name string, fn func(ctx context.Context) error, opts ...xtrace.SpanOption,
) error {
	//nolint:wrapcheck // fn's error is returned as is
	return c.n.Scope().Run(ctx, name, func(ctx context.Context, _ trace.Span) error { return fn(ctx) }, opts...)
}

// Go runs fn under the service lifecycle.
func (c Component) Go(fn func(ctx context.Context) error) { c.n.Go(fn) }

// OnStart hooks fn into the component's start.
func (c Component) OnStart(fn func(ctx context.Context) error) { c.n.OnStart(fn) }

// OnStop hooks fn into the component's stop.
func (c Component) OnStop(fn func(ctx context.Context) error) { c.n.OnStop(fn) }

func (c Component) node() *tree.Node { return c.n }
