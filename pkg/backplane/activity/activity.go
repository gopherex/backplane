// Package activity declares operations a service implements ("умею"): the
// targets of bindings and rules. Handle declares one run as a Temporal
// activity, Workflow one run as a child workflow; both are named as
// declared on the service's task queue, input and output enveloped in
// backplane.v1.ActivityCall / ActivityResult.
//
// Inputs and outputs evolve additively: a reader ignores fields it does not
// know (protojson with DiscardUnknown), a missing field reads as its zero
// value, and renaming a field means adding a new one and keeping the old
// until every reader uses the new.
//
//	activity.Handle(root, "Send", smtp.Send, activity.StartToClose(10*time.Second),
//	    activity.Retry(activity.RetryHint{Attempts: 5}), activity.Describe("sends a mail"))
package activity

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/temporal"
)

// Option sets what the manifest tells a binding about the activity: its
// defaults for a step calling it. The binding may override them; the
// handler does not enforce them.
type Option interface{ apply(a *backplanev1.Activity) }

type optionFunc func(a *backplanev1.Activity)

func (f optionFunc) apply(a *backplanev1.Activity) { f(a) }

// StartToClose is how long one attempt may take (for a workflow-backed
// activity, one run).
func StartToClose(d time.Duration) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.StartToClose = positive(d) })
}

// HeartbeatTimeout: an attempt that does not Heartbeat within d is
// considered lost and retried. Only for activities that call Heartbeat.
func HeartbeatTimeout(d time.Duration) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.Heartbeat = positive(d) })
}

// RetryHint is the retry a binding step should use by default.
type RetryHint struct {
	// Attempts in total, the first included; 0 is the platform's default.
	Attempts uint32
}

// Retry sets the default retry of a step calling the activity.
func Retry(h RetryHint) Option {
	return optionFunc(func(a *backplanev1.Activity) {
		a.Retry = &backplanev1.RetryPolicy{Attempts: h.Attempts}
	})
}

// Describe is the activity's description for the console.
func Describe(s string) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.Description = s })
}

func positive(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}

	return durationpb.New(d)
}

// Handle declares an activity and its implementation. name is CamelCase
// ([A-Z][A-Za-z0-9]*), anything else panics. fn's ctx carries InfoOf.
func Handle[Req, Res any](
	scope deps.Scope, name string, fn func(ctx context.Context, in Req) (Res, error), opts ...Option,
) {
	e := declare[Req, Res](scope, name, backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, opts)
	e.Activity(name, func(ctx context.Context, in []byte) ([]byte, error) {
		var req Req
		if err := decl.Decode(in, &req); err != nil {
			return nil, env.NonRetryableError{Err: fmt.Errorf("activity %s: decode: %w", name, err)}
		}

		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}

		return decl.Encode(res)
	})
}

// Workflow declares a workflow-backed activity: a binding step runs fn as
// a child workflow of type name on the service's queue, so the activity
// may itself wait, sleep, call hooks (WorkflowCall) and run activities.
// name is CamelCase; errors map as for Handle (NonRetryable included).
func Workflow[Req, Res any](
	scope deps.Scope, name string, fn func(ctx workflow.Context, in Req) (Res, error), opts ...Option,
) {
	e := declare[Req, Res](scope, name, backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW, opts)

	run := temporal.WorkflowActivity(e.Service, name, func(ctx workflow.Context, in []byte) ([]byte, error) {
		var req Req
		if err := decl.Decode(in, &req); err != nil {
			return nil, env.NonRetryableError{Err: fmt.Errorf("activity %s: decode: %w", name, err)}
		}

		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}

		return decl.Encode(res)
	})

	e.RegisterWorker(func(registry any) {
		if r, ok := registry.(worker.Registry); ok {
			r.RegisterWorkflowWithOptions(run, workflow.RegisterOptions{Name: name})
		}
	})
}

// declare records the activity in the manifest.
func declare[Req, Res any](
	scope deps.Scope, name string, kind backplanev1.ActivityKind, opts []Option,
) *env.Env {
	e, _ := decl.Env(scope, "activity "+name)
	temporal.CheckName("activity", name)

	a := &backplanev1.Activity{
		Name:   name,
		Kind:   kind,
		Input:  decl.Schema[Req](e.Service, "activity_"+name+"_in"),
		Output: decl.Schema[Res](e.Service, "activity_"+name+"_out"),
	}
	for _, opt := range opts {
		opt.apply(a)
	}

	e.Manifest.Activity(a)

	return e
}

// NonRetryable marks err as final: the binding does not retry the activity
// (on Temporal, a non-retryable application error). Any other error is
// retried by the binding's retry policy; a *temporal.ApplicationError is
// passed as is, so its own retryability applies. nil stays nil.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}

	return env.NonRetryableError{Err: err}
}
