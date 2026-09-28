// Package tree is the service's node tree: components, dependencies and
// singletons, each with its own name, logger and instrumentation scope. The
// tree is one lifecycle component: nodes start in creation order and stop in
// reverse.
package tree

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"
	"github.com/gopherex/xtrace"

	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
)

// Kind of node.
type Kind int

// Node kinds.
const (
	Root Kind = iota
	Component
	Dependency
	Singleton
)

// Tree holds nodes in creation order.
type Tree struct {
	log *xlog.Logger

	mu      sync.Mutex
	nodes   []*Node
	started bool
	group   lifecycle.Group
}

// Node is one element of the tree.
type Node struct {
	tree     *Tree
	name     string
	path     string
	kind     Kind
	optional bool
	log      *xlog.Logger
	scope    *xtrace.Scope

	mu     sync.Mutex
	starts []func(ctx context.Context) error
	stops  []func(ctx context.Context) error
	jobs   []func(ctx context.Context) error
	ready  probe.Probe
}

// Info describes a node for the manifest.
type Info struct {
	Path     string
	Kind     Kind
	Optional bool
}

// New creates a tree whose root node is named after the service.
func New(service string, log *xlog.Logger) (*Tree, *Node) {
	t := &Tree{log: log}
	root := &Node{tree: t, name: service, kind: Root, log: log, scope: xtrace.New(service)}
	t.nodes = append(t.nodes, root)

	return t, root
}

// Child creates a node under n. Names are unique among siblings: a
// duplicate gets a numeric suffix.
func (n *Node) Child(name string, kind Kind, optional bool) *Node {
	t := n.tree
	t.mu.Lock()
	defer t.mu.Unlock()

	path := name
	if n.kind != Root {
		path = n.path + "/" + name
	}

	for i, base := 2, path; t.has(path); i++ {
		path = base + "-" + strconv.Itoa(i)
	}

	child := &Node{
		tree: t, name: name, path: path, kind: kind, optional: optional,
		log:   n.tree.log.With(xlog.String("node", path)),
		scope: xtrace.New(n.scope.Name() + "/" + path),
	}
	t.nodes = append(t.nodes, child)

	return child
}

func (t *Tree) has(path string) bool {
	for _, n := range t.nodes {
		if n.path == path {
			return true
		}
	}

	return false
}

// Name of the node.
func (n *Node) Name() string { return n.name }

// Path of the node in the tree ("" for the root).
func (n *Node) Path() string { return n.path }

// Log is the node's logger (records carry node=<path>).
func (n *Node) Log() *xlog.Logger { return n.log }

// Scope is the node's instrumentation scope.
func (n *Node) Scope() *xtrace.Scope { return n.scope }

// OnStart adds fn to the node's start, run when the tree reaches the node.
func (n *Node) OnStart(fn func(ctx context.Context) error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.starts = append(n.starts, fn)
}

// OnStop adds fn to the node's stop; stops run in reverse order.
func (n *Node) OnStop(fn func(ctx context.Context) error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.stops = append(n.stops, fn)
}

// Go runs fn under the service lifecycle once the tree has started; its
// error stops the service.
func (n *Node) Go(fn func(ctx context.Context) error) {
	t := n.tree
	t.mu.Lock()
	started, group := t.started, t.group
	t.mu.Unlock()

	if started {
		group.Go(n.jobName(), fn)

		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.jobs = append(n.jobs, fn)
}

func (n *Node) jobName() string {
	if n.path == "" {
		return "root"
	}

	return n.path
}

// Ready makes p part of the service readiness.
func (n *Node) Ready(p probe.Probe) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.ready = p
}

// Name implements lifecycle.Component.
func (*Tree) Name() string { return "tree" }

// Start runs node starts in creation order, then node goroutines.
func (t *Tree) Start(ctx context.Context, g lifecycle.Group) error {
	t.mu.Lock()
	nodes := append([]*Node(nil), t.nodes...)
	t.mu.Unlock()

	for _, n := range nodes {
		n.mu.Lock()
		starts := append([]func(context.Context) error(nil), n.starts...)
		n.mu.Unlock()

		for _, start := range starts {
			if err := start(ctx); err != nil {
				return fmt.Errorf("%s: %w", n.jobName(), err)
			}
		}
	}

	t.mu.Lock()
	t.started, t.group = true, g
	t.mu.Unlock()

	for _, n := range nodes {
		n.mu.Lock()
		jobs := n.jobs
		n.jobs = nil
		n.mu.Unlock()

		for _, job := range jobs {
			g.Go(n.jobName(), job)
		}
	}

	return nil
}

// Stop runs node stops in reverse creation order.
func (t *Tree) Stop(ctx context.Context) error {
	t.mu.Lock()
	nodes := append([]*Node(nil), t.nodes...)
	t.mu.Unlock()

	var errs []error

	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		n.mu.Lock()
		stops := append([]func(context.Context) error(nil), n.stops...)
		n.mu.Unlock()

		for j := len(stops) - 1; j >= 0; j-- {
			if err := stops[j](ctx); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n.jobName(), err))
			}
		}
	}

	return errors.Join(errs...)
}

// Probes are the readiness probes of all nodes.
func (t *Tree) Probes() []probe.Probe {
	t.mu.Lock()
	defer t.mu.Unlock()

	var probes []probe.Probe

	for _, n := range t.nodes {
		n.mu.Lock()
		if n.ready != nil {
			probes = append(probes, n.ready)
		}
		n.mu.Unlock()
	}

	return probes
}

// Nodes describes every node but the root, in creation order.
func (t *Tree) Nodes() []Info {
	t.mu.Lock()
	defer t.mu.Unlock()

	infos := make([]Info, 0, len(t.nodes))
	for _, n := range t.nodes {
		if n.kind != Root {
			infos = append(infos, Info{Path: n.path, Kind: n.kind, Optional: n.optional})
		}
	}

	return infos
}
