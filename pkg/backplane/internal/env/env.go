// Package env is what every node of one service shares: the manifest being
// declared into and the wiring of events, hooks and activities to a
// transport. Declaration packages reach it through the node a Scope wraps,
// so nothing is process-global.
package env

import (
	"context"
	"errors"
	"sync"

	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// ErrUnavailable: no transport carries this call (yet).
var ErrUnavailable = errors.New("backplane: transport unavailable")

// Transport carries events and hook calls; payloads are encoded JSON. The
// SDK installs one when NATS/Temporal are wired; tests install a recorder.
type Transport interface {
	Publish(ctx context.Context, event, key string, payload []byte) error
	Call(ctx context.Context, hook string, in []byte) ([]byte, error)
}

// Handler serves an activity or a reactor; payloads are encoded JSON.
type Handler func(ctx context.Context, in []byte) ([]byte, error)

// Env of one service.
type Env struct {
	Service  string
	Manifest *manifest.Builder

	mu         sync.RWMutex
	transport  Transport
	activities map[string]Handler
	reactors   map[string]Handler // by consumer
}

// New creates the env of service.
func New(service string, m *manifest.Builder) *Env {
	return &Env{Service: service, Manifest: m, activities: map[string]Handler{}, reactors: map[string]Handler{}}
}

// Of returns the env of the tree n belongs to.
func Of(n *node.Node) *Env {
	e, _ := n.Env().(*Env)

	return e
}

// SetTransport installs the transport; nil removes it.
func (e *Env) SetTransport(t Transport) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.transport = t
}

// Transport is the installed transport or nil.
func (e *Env) Transport() Transport {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.transport
}

// Activity stores the handler of a declared activity.
func (e *Env) Activity(name string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.activities[name] = h
}

// Reactor stores the handler of a declared reactor.
func (e *Env) Reactor(consumer string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.reactors[consumer] = h
}

// ActivityHandler returns the handler of a declared activity.
func (e *Env) ActivityHandler(name string) (Handler, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	h, ok := e.activities[name]

	return h, ok
}

// ReactorHandler returns the handler of a declared reactor.
func (e *Env) ReactorHandler(consumer string) (Handler, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	h, ok := e.reactors[consumer]

	return h, ok
}
