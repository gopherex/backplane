// Package activity declares operations a service implements ("умею"): the
// targets of bindings and rules. They run as Temporal activities on the
// service's task queue (M2); declaring records them in the manifest now.
package activity

import (
	"context"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
)

// Handle declares an activity and its implementation.
//
// Only the declaration is recorded until M2, when fn is registered with the
// service's Temporal worker.
func Handle[Req, Res any](svc backplane.Owner, name string, fn func(context.Context, Req) (Res, error)) {
	a := &backplanev1.Activity{
		Name:   name,
		Input:  decl.Schema[Req](svc.Name(), "activity_"+name+"_in"),
		Output: decl.Schema[Res](svc.Name(), "activity_"+name+"_out"),
	}
	decl.To(svc).Activity(a)

	_ = fn
}
