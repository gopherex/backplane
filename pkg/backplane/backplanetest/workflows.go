package backplanetest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nexus-rpc/sdk-go/nexus"
	tactivity "go.temporal.io/sdk/activity"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	internal "github.com/gopherex/backplane/pkg/backplane/internal/temporal"
)

// Workflows is a new Temporal test environment (go.temporal.io/sdk/
// testsuite) set up as the service's worker is: every activity declared
// with activity.Handle (by name, over the envelope), every workflow of
// workflows.Declare and activity.Workflow, and what workflows.Register
// registers. The service's hooks are a Nexus service of the environment
// answered by Answer, so hook.WorkflowCall works in workflow code; an
// unanswered hook fails with hook.ErrNoBinding, a failing answer with its
// message. Declare everything first; Start the harness first when the
// handlers use dependencies. An environment runs one workflow: call
// Workflows again for the next.
//
//	w := backplanetest.Workflows(h)
//	w.ExecuteWorkflow("Welcome", in)
//	err := w.GetWorkflowResult(&out)
func Workflows(h *Harness) *testsuite.TestWorkflowEnvironment {
	h.t.Helper()

	var suite testsuite.WorkflowTestSuite

	suite.SetLogger(tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)))

	w := suite.NewTestWorkflowEnvironment()

	for name, handler := range h.env.Activities() {
		w.RegisterActivityWithOptions(internal.ActivityFunc(h.env.Service, name, handler),
			tactivity.RegisterOptions{Name: name})
	}

	for _, register := range h.env.WorkerRegistrations() {
		register(w)
	}

	m, err := h.env.Manifest.Build()
	if err != nil {
		h.t.Fatalf("backplanetest: manifest: %v", err)
	}

	if len(m.GetHooks()) > 0 {
		w.RegisterNexusService(hooks(h, m.GetHooks()))
	}

	return w
}

// hooks is the Nexus service <service>.Hooks of the environment: one sync
// operation per declared hook, answered by the recorder. Failures are what
// backplane fails a hook call with.
func hooks(h *Harness, declared []*backplanev1.Hook) *nexus.Service {
	s := nexus.NewService(h.env.Service + internal.HooksSuffix)

	for _, d := range declared {
		full := h.env.Service + "." + d.GetName()
		operation := nexus.NewSyncOperation(d.GetName(), func(
			ctx context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
		) (*backplanev1.HookResult, error) {
			out, err := h.rec.Call(ctx, full, call.GetPayload())

			switch {
			case errors.Is(err, env.ErrUnavailable):
				return nil, temporal.NewNonRetryableApplicationError("no binding", internal.NoBindingType, nil)
			case err != nil:
				return nil, temporal.NewNonRetryableApplicationError(err.Error(), internal.HookFailedType, nil)
			}

			return &backplanev1.HookResult{Payload: out}, nil
		})

		if err := s.Register(operation); err != nil {
			h.t.Fatalf("backplanetest: hook %s: %v", full, err)
		}
	}

	return s
}

// WorkflowActivity runs the workflow-backed activity name (activity.Workflow)
// in w as a binding step would start it, and returns its result. w is
// from Workflows; it runs this one workflow.
func WorkflowActivity[Req, Res any](w *testsuite.TestWorkflowEnvironment, name string, in Req) (Res, error) {
	var out Res

	raw, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("backplanetest: encode: %w", err)
	}

	w.ExecuteWorkflow(name, &backplanev1.ActivityCall{Activity: name, Payload: raw})

	if err := w.GetWorkflowError(); err != nil {
		return out, fmt.Errorf("backplanetest: %s: %w", name, err)
	}

	var res backplanev1.ActivityResult
	if err := w.GetWorkflowResult(&res); err != nil {
		return out, fmt.Errorf("backplanetest: %s result: %w", name, err)
	}

	if err := decl.Decode(res.GetPayload(), &out); err != nil {
		return out, fmt.Errorf("backplanetest: decode: %w", err)
	}

	return out, nil
}
