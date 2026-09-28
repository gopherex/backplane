// Package hook declares operations a service calls without implementing
// them ("нужно"). Who answers is a binding in backplane; the call is a
// Temporal Nexus operation <Name> of service <service>.Hooks on endpoint
// <service>. Call works from any Go code; WorkflowCall is the same call from
// workflow code. Without Temporal Call returns ErrUnavailable.
package hook

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/temporal"
)

var (
	// ErrUnavailable is returned while no transport carries hook calls.
	ErrUnavailable = env.ErrUnavailable
	// ErrNoBinding: backplane has no binding for the hook.
	ErrNoBinding = env.ErrNoBinding
)

// Ref is a declared hook. The zero Ref is undeclared: Call fails.
type Ref[Req, Res any] struct {
	env  *env.Env
	name string
}

// Option configures Declare.
type Option interface{ apply(o *options) }

type options struct{ required bool }

type required struct{}

func (required) apply(o *options) { o.required = true }

// Required marks the hook as one the service cannot work without.
func Required() Option { return required{} }

// Declare registers a hook in the manifest. Schemas are reflected from Req
// and Res when possible; without them the payload is plain JSON (protojson
// for proto messages).
func Declare[Req, Res any](scope deps.Scope, name string, opts ...Option) Ref[Req, Res] {
	e, _ := decl.Env(scope, "hook "+name)

	var o options
	for _, opt := range opts {
		opt.apply(&o)
	}

	e.Manifest.Hook(&backplanev1.Hook{
		Name:     name,
		Input:    decl.Schema[Req](e.Service, "hook_"+name+"_in"),
		Output:   decl.Schema[Res](e.Service, "hook_"+name+"_out"),
		Required: o.required,
	})

	return Ref[Req, Res]{env: e, name: name}
}

// Name is the full hook name <service>.<Name>.
func (r Ref[Req, Res]) Name() string {
	if r.env == nil {
		return ""
	}

	return r.env.Service + "." + r.name
}

// Call raises the hook and waits for the bound implementation within ctx
// (30s when ctx has no deadline). Outside workflow code only: in a workflow
// use WorkflowCall.
func (r Ref[Req, Res]) Call(ctx context.Context, in Req) (Res, error) {
	var out Res

	if r.env == nil {
		return out, fmt.Errorf("hook: call on an undeclared Ref: %w", ErrUnavailable)
	}

	t := r.env.Caller()
	if t == nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), ErrUnavailable)
	}

	payload, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("hook %s: encode: %w", r.Name(), err)
	}

	res, err := t.Call(ctx, r.Name(), payload)
	if err != nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), err)
	}

	if err := decl.Decode(res, &out); err != nil {
		return out, fmt.Errorf("hook %s: decode: %w", r.Name(), err)
	}

	return out, nil
}

// WorkflowCall raises the hook from workflow code: the Nexus operation
// directly, no extra workflow. The rest of the run's timeout is the call's
// deadline. The error is readable ("hook <service>.<Name>: no binding")
// and matches ErrNoBinding when there is no binding.
func (r Ref[Req, Res]) WorkflowCall(ctx workflow.Context, in Req) (Res, error) {
	var out Res

	if r.env == nil {
		return out, fmt.Errorf("hook: call on an undeclared Ref: %w", ErrUnavailable)
	}

	payload, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("hook %s: encode: %w", r.Name(), err)
	}

	res, err := temporal.ExecuteHook(ctx, &backplanev1.HookCall{
		Hook:     r.Name(),
		Payload:  payload,
		Trace:    temporal.WorkflowTrace(ctx),
		Deadline: temporal.WorkflowDeadline(ctx),
	})
	if err != nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), temporal.Local(err))
	}

	if err := decl.Decode(res.GetPayload(), &out); err != nil {
		return out, fmt.Errorf("hook %s: decode: %w", r.Name(), err)
	}

	return out, nil
}
