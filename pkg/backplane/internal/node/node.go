// Package node is the service's one lifecycle model: a tree of nodes, each
// with a name, a logger, an instrumentation scope, start and stop hooks, its
// own goroutines and an optional readiness probe.
//
// The SDK's own parts (config, listeners, presence) and the author's
// components and dependencies are nodes of the same tree. Start walks the
// tree depth-first in creation order; Stop walks it back in exact reverse, so
// a node always stops before anything created ahead of it — a component
// before the dependency it uses, a child before its parent.
//
// Stopping a node cancels its context, runs its stop hooks in reverse, then
// waits for its goroutines. A node whose start failed is stopped too: stop
// hooks must tolerate a partial start.
package node

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"
	"github.com/gopherex/xtrace"
)

// Errors.
var (
	// ErrStopTimeout: a node's goroutines did not return within the stop
	// budget.
	ErrStopTimeout = errors.New("node: goroutines did not stop in time")
	// ErrPanic: a node's goroutine panicked.
	ErrPanic = errors.New("node: goroutine panicked")
)

// Kind of node.
type Kind int

// Node kinds. System nodes are the SDK's own; Root is the author's tree.
const (
	System Kind = iota
	Root
	Component
	Dependency
	Singleton
)

type status int

const (
	pending status = iota
	starting
	started
	failed
	stopping
	stopped
)

type phase int

const (
	building phase = iota
	running        // Start was called; nodes may be created only by a starting parent
	done           // Stop was called
)

// Tree is shared by every node of one service.
type Tree struct {
	service string
	log     *xlog.Logger
	env     any
	failed  chan error

	mu    sync.Mutex
	phase phase
}

// Node is one element of the tree.
type Node struct {
	tree     *Tree
	parent   *Node
	ns       *namespace
	name     string
	path     string
	kind     Kind
	optional bool
	log      *xlog.Logger
	scope    *xtrace.Scope

	mu       sync.Mutex
	status   status
	children []*Node
	starts   []func(ctx context.Context) error
	stops    []func(ctx context.Context) error
	jobs     []func(ctx context.Context) error
	ready    probe.Probe
	ctx      context.Context //nolint:containedctx // the node's lifetime, handed to its goroutines
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// namespace keeps paths unique under a Root (or under the service root for
// system nodes).
type namespace struct {
	mu    sync.Mutex
	paths map[string]bool
}

// New creates a tree and returns its service root. env is service-wide data
// any node can reach through Env.
func New(service string, log *xlog.Logger, env any) *Node {
	t := &Tree{service: service, log: log, env: env, failed: make(chan error, 1)}

	return &Node{
		tree: t, ns: &namespace{paths: map[string]bool{}}, name: service, kind: System,
		log: log, scope: xtrace.New(service),
	}
}

// Child creates a node under n. Names are unique within a namespace: a
// duplicate path gets a numeric suffix. After Start only a starting parent
// may create children (a dependency building its internals); anything else
// is a programming error and panics.
func (n *Node) Child(name string, kind Kind, optional bool) *Node {
	t := n.tree

	t.mu.Lock()
	ph := t.phase
	t.mu.Unlock()

	n.mu.Lock()
	defer n.mu.Unlock()

	if ph != building && n.status != starting {
		panic(fmt.Sprintf("backplane: node %q created under %q after the service started", name, n.label()))
	}

	ns, path := n.ns, name
	switch {
	case kind == Root:
		ns, path = &namespace{paths: map[string]bool{}}, ""
	case n.kind != System && n.kind != Root:
		path = n.path + "/" + name
	}

	path = ns.claim(path)

	scope := t.service
	if path != "" {
		scope += "/" + path
	}

	child := &Node{
		tree: t, parent: n, ns: ns, name: name, path: path, kind: kind, optional: optional,
		scope: xtrace.New(scope),
	}

	switch kind {
	case System:
		child.log = t.log.With(xlog.String("component", name))
	case Root:
		child.log = t.log
	default:
		child.log = t.log.With(xlog.String("node", path))
	}

	n.children = append(n.children, child)

	return child
}

func (ns *namespace) claim(path string) string {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	for i, base := 2, path; ns.paths[path]; i++ {
		path = base + "-" + strconv.Itoa(i)
	}

	ns.paths[path] = true

	return path
}

// Name of the node.
func (n *Node) Name() string { return n.name }

// Path of the node within its namespace ("" for a root).
func (n *Node) Path() string { return n.path }

// Kind of the node.
func (n *Node) Kind() Kind { return n.kind }

// Optional reports whether the node is an optional dependency.
func (n *Node) Optional() bool { return n.optional }

// Log is the node's logger.
func (n *Node) Log() *xlog.Logger { return n.log }

// Scope is the node's instrumentation scope.
func (n *Node) Scope() *xtrace.Scope { return n.scope }

// Env is the service-wide data given to New.
func (n *Node) Env() any { return n.tree.env }

func (n *Node) label() string {
	switch {
	case n.path != "":
		return n.path
	case n.kind == Root:
		return "root"
	default:
		return n.name
	}
}

// OnStart adds fn to the node's start. Only before the node starts.
func (n *Node) OnStart(fn func(ctx context.Context) error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.status != pending && n.status != starting {
		panic(fmt.Sprintf("backplane: OnStart on %q after it started", n.label()))
	}

	n.starts = append(n.starts, fn)
}

// OnStop adds fn to the node's stop; stop hooks run in reverse.
func (n *Node) OnStop(fn func(ctx context.Context) error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.status >= stopping {
		panic(fmt.Sprintf("backplane: OnStop on %q after it stopped", n.label()))
	}

	n.stops = append(n.stops, fn)
}

// Go runs fn on the node's context once the node has started; it is
// cancelled when the node stops, and the stop waits for it. A non-nil error
// (or a panic) stops the service. After the node stopped, fn is dropped.
func (n *Node) Go(fn func(ctx context.Context) error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch n.status {
	case pending, starting:
		n.jobs = append(n.jobs, fn)
	case started:
		n.launch(fn)
	default:
		n.log.Warn("goroutine dropped: node is stopping")
	}
}

// launch runs fn; n.mu is held.
func (n *Node) launch(fn func(ctx context.Context) error) {
	n.wg.Add(1)

	go func() {
		defer n.wg.Done()

		if err := n.guard(fn); err != nil && n.ctx.Err() == nil {
			n.log.Error("goroutine failed", xlog.Err(err))
			n.tree.fail(fmt.Errorf("%s: %w", n.label(), err))
		}
	}()
}

func (n *Node) guard(fn func(ctx context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v\n%s", ErrPanic, r, debug.Stack())
		}
	}()

	return fn(n.ctx)
}

func (t *Tree) fail(err error) {
	select {
	case t.failed <- err:
	default:
	}
}

// Failed delivers the first goroutine failure of the tree.
func (n *Node) Failed() <-chan error { return n.tree.failed }

// Ready makes p part of the service readiness.
func (n *Node) Ready(p probe.Probe) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.ready = p
}

// Start starts n's subtree depth-first in creation order. ctx bounds the
// start hooks; node lifetimes are detached from it. On error the caller
// must still call Stop, which unwinds whatever started.
func (n *Node) Start(ctx context.Context) error {
	n.tree.mu.Lock()
	n.tree.phase = running
	n.tree.mu.Unlock()

	return n.start(ctx)
}

func (n *Node) start(ctx context.Context) error {
	n.mu.Lock()
	n.ctx, n.cancel = context.WithCancel(context.WithoutCancel(ctx))
	n.status = starting
	starts := n.starts
	n.mu.Unlock()

	for _, fn := range starts {
		if err := fn(ctx); err != nil {
			n.setStatus(failed)

			return fmt.Errorf("%s: %w", n.label(), err)
		}
	}

	n.mu.Lock()
	n.status = started
	jobs := n.jobs
	n.jobs = nil

	for _, fn := range jobs {
		n.launch(fn)
	}

	n.mu.Unlock()

	for _, c := range n.snapshot() {
		if err := c.start(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (n *Node) setStatus(s status) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.status = s
}

func (n *Node) snapshot() []*Node {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]*Node(nil), n.children...)
}

// Stop stops n's subtree in exact reverse of Start within ctx. Nodes that
// never started are skipped.
func (n *Node) Stop(ctx context.Context) error {
	n.tree.mu.Lock()
	n.tree.phase = done
	n.tree.mu.Unlock()

	return n.stop(ctx)
}

func (n *Node) stop(ctx context.Context) error {
	var errs []error

	children := n.snapshot()
	for i := len(children) - 1; i >= 0; i-- {
		errs = append(errs, children[i].stop(ctx))
	}

	n.mu.Lock()

	st := n.status
	if st == started || st == failed {
		n.status = stopping
	} else {
		n.status = stopped
	}

	stops := n.stops
	n.mu.Unlock()

	if st != started && st != failed {
		return errors.Join(errs...)
	}

	n.cancel()

	for i := len(stops) - 1; i >= 0; i-- {
		if err := stops[i](ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n.label(), err))
		}
	}

	errs = append(errs, n.wait(ctx))
	n.setStatus(stopped)

	return errors.Join(errs...)
}

func (n *Node) wait(ctx context.Context) error {
	drained := make(chan struct{})

	go func() {
		n.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", n.label(), ErrStopTimeout)
	}
}

// Walk visits n's subtree depth-first in creation order.
func (n *Node) Walk(fn func(*Node)) {
	fn(n)

	for _, c := range n.snapshot() {
		c.Walk(fn)
	}
}

// Readiness is the readiness of n's subtree, evaluated on every check so
// nodes created during start count too.
func (n *Node) Readiness() probe.Probe {
	return probe.Func(func(ctx context.Context) probe.Status {
		var probes []probe.Probe

		n.Walk(func(c *Node) {
			c.mu.Lock()
			if c.ready != nil {
				probes = append(probes, c.ready)
			}
			c.mu.Unlock()
		})

		return probe.All(probes...).Check(ctx)
	})
}

// Group runs goroutines owned by a node; *Node is one.
type Group interface {
	Go(fn func(ctx context.Context) error)
}
