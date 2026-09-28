// Package hook declares operations a service calls without implementing them
// ("нужно"). Who answers is a binding in backplane. Transport arrives with
// Temporal Nexus (M2); until then Call returns ErrUnavailable.
package hook

import (
	"context"
	"errors"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
)

// ErrUnavailable is returned while no transport is configured.
var ErrUnavailable = errors.New("hook: transport unavailable")

// Ref is a declared hook.
type Ref[Req, Res any] struct {
	svc  backplane.Owner
	name string
}

// Option configures Declare.
type Option func(*backplanev1.Hook)

// Required marks the hook as one the service cannot work without.
func Required() Option { return func(h *backplanev1.Hook) { h.Required = true } }

// Declare registers a hook in the manifest, on the Root inside the State
// constructor or on the Service after Open. Schemas are reflected from Req
// and Res when possible; without them the payload is plain JSON.
func Declare[Req, Res any](svc backplane.Owner, name string, opts ...Option) *Ref[Req, Res] {
	h := &backplanev1.Hook{
		Name:   name,
		Input:  decl.Schema[Req](svc.Name(), "hook_"+name+"_in"),
		Output: decl.Schema[Res](svc.Name(), "hook_"+name+"_out"),
	}
	for _, o := range opts {
		o(h)
	}

	decl.To(svc).Hook(h)

	return &Ref[Req, Res]{svc: svc, name: name}
}

// Name is the full hook name <service>.<Name>.
func (r *Ref[Req, Res]) Name() string { return r.svc.Name() + "." + r.name }

// Call raises the hook and waits for the bound implementation.
//
// Not wired until M2 (Temporal Nexus): always returns ErrUnavailable.
func (r *Ref[Req, Res]) Call(context.Context, Req) (Res, error) {
	var zero Res
	return zero, ErrUnavailable
}
