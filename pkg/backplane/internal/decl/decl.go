// Package decl connects the hook, activity and event packages to the
// service they declare into, without putting the declaration entry points on
// the public Service API, and derives optional schemas for declarations.
package decl

import (
	"sync"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// Sink receives declarations.
type Sink interface {
	Hook(h *backplanev1.Hook)
	Activity(a *backplanev1.Activity)
	Event(e *backplanev1.Event)
}

// The registry is process-wide on purpose: it is how declaration packages
// reach the service they declare into without a public entry point.
//
//nolint:gochecknoglobals // see above
var (
	registryMu sync.Mutex
	sinks      = map[any]Sink{}
)

// Attach binds a service to its sink; the service calls it in New.
func Attach(owner any, s Sink) {
	registryMu.Lock()
	defer registryMu.Unlock()

	sinks[owner] = s
}

// Detach removes the binding; the service calls it when Run returns.
func Detach(owner any) {
	registryMu.Lock()
	defer registryMu.Unlock()

	delete(sinks, owner)
}

// To returns the sink of owner; a service created without New has none and
// its declarations are dropped.
func To(owner any) Sink {
	registryMu.Lock()
	defer registryMu.Unlock()

	if s, ok := sinks[owner]; ok {
		return s
	}

	return discard{}
}

type discard struct{}

func (discard) Hook(*backplanev1.Hook)         {}
func (discard) Activity(*backplanev1.Activity) {}
func (discard) Event(*backplanev1.Event)       {}

// Schema reflects T under <service>/<name>@1.0.0, or nil when T cannot be
// described: the declaration stays, the payload is plain JSON.
func Schema[T any](service, name string) *sp.Schema {
	s, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	if err != nil {
		return nil
	}

	return s
}
