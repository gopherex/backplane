// Package activity declares operations a service implements ("умею"): the
// targets of bindings and rules. They run as Temporal activities named as
// declared on the service's task queue, input and output enveloped in
// backplane.v1.ActivityCall / ActivityResult.
package activity

import (
	"context"
	"fmt"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
)

// Handle declares an activity and its implementation.
func Handle[Req, Res any](scope deps.Scope, name string, fn func(ctx context.Context, in Req) (Res, error)) {
	e, _ := decl.Env(scope, "activity "+name)
	e.Manifest.Activity(&backplanev1.Activity{
		Name:   name,
		Input:  decl.Schema[Req](e.Service, "activity_"+name+"_in"),
		Output: decl.Schema[Res](e.Service, "activity_"+name+"_out"),
	})
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
