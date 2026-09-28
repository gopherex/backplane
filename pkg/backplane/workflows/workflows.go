// Package workflows gives a service its own Temporal workflows (design §9):
// the SDK owns the connection and the worker on the service's task queue
// (= its name); the author registers on it and uses the client. Between
// services go through hooks, never through another service's queue.
//
//	workflows.Register(root, func(r worker.Registry) {
//	    r.RegisterWorkflow(orders.Fulfil)
//	    r.RegisterActivity(orders.Charge)
//	})
//
//	c, err := workflows.Client(scope)
//	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: workflows.Queue(scope)}, orders.Fulfil, in)
package workflows

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
)

// Register adds fn's workflows and activities to the service's worker.
// Declare it before Run, like any declaration.
func Register(scope deps.Scope, fn func(r worker.Registry)) {
	e, _ := decl.Env(scope, "workflows")
	if e.Manifest.Sealed() {
		panic("backplane: workflows registered after Run")
	}

	e.RegisterWorker(func(registry any) {
		if r, ok := registry.(worker.Registry); ok {
			fn(r)
		}
	})
}

// Client is the service's Temporal client. It fails with an error wrapping
// hook.ErrUnavailable while Temporal is not configured or not connected.
func Client(scope deps.Scope) (client.Client, error) { //nolint:ireturn // the Temporal client is an interface
	e, _ := decl.Env(scope, "workflows client")

	c, err := e.WorkflowClient()
	if err != nil {
		return nil, fmt.Errorf("workflows: %w", err)
	}

	tc, ok := c.(client.Client)
	if !ok {
		return nil, fmt.Errorf("workflows: unexpected client %T", c)
	}

	return tc, nil
}

// Queue is the service's task queue.
func Queue(scope deps.Scope) string {
	e, _ := decl.Env(scope, "workflows queue")

	return e.Service
}
