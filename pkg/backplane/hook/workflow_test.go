package hook_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/hook"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	internal "github.com/gopherex/backplane/pkg/backplane/internal/temporal"
	"github.com/gopherex/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

// outcome is what the test workflow saw.
type outcome struct {
	Total     int    `json:"total"`
	NoBinding bool   `json:"no_binding"`
	Err       string `json:"err"`
}

// price plays backplane's binding of <svc>.price: doubles the amount,
// declines negatives, has no binding for zero.
func price() nexus.Operation[*backplanev1.HookCall, *backplanev1.HookResult] {
	return nexus.NewSyncOperation("price", func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		var in Quote
		if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
			return nil, nexus.NewOperationFailedErrorf("bad payload")
		}

		switch {
		case in.Amount == 0:
			return nil, temporal.NewNonRetryableApplicationError("no binding", internal.NoBindingType, nil)
		case in.Amount < 0:
			return nil, nexus.NewOperationFailedErrorf("%w", errDeclined)
		}

		out, _ := json.Marshal(Price{Total: in.Amount * 2})

		return &backplanev1.HookResult{Payload: out}, nil
	})
}

func TestWorkflowCall(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("wfcall")
	e := env.New(svc, manifest.New(svc, "0.0.0"))
	app := node.New(svc, testlog.Discard(), e).Child(svc, node.Root, false)
	root := link.Scope(app).(deps.Component)
	ref := hook.Declare[Quote, Price](root, "price")

	tc := temporaltest.Dial(t)
	temporaltest.Hooks(t, tc, svc, price())

	// A workflow returns an error last, whether it uses it or not.
	quote := func(ctx workflow.Context, amount int) (outcome, error) { //nolint:unparam // workflow signature
		out, err := ref.WorkflowCall(ctx, Quote{Amount: amount})
		if err != nil {
			return outcome{NoBinding: errors.Is(err, hook.ErrNoBinding), Err: err.Error()}, nil
		}

		return outcome{Total: out.Total}, nil
	}

	queue := "wf-" + svc
	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(quote, workflow.RegisterOptions{Name: "quote"})
	})

	for _, step := range []struct {
		amount int
		want   outcome
	}{
		{21, outcome{Total: 42}},
		{0, outcome{NoBinding: true, Err: "hook " + svc + ".price: no binding"}},
		{-1, outcome{Err: "hook " + svc + ".price: declined"}},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)

		run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			TaskQueue: queue, WorkflowExecutionTimeout: 15 * time.Second,
		}, "quote", step.amount)
		if err != nil {
			cancel()
			t.Fatal(err)
		}

		var got outcome

		err = run.Get(ctx, &got)

		cancel()

		if err != nil || got != step.want {
			t.Errorf("amount %d: %+v %v, want %+v", step.amount, got, err, step.want)
		}
	}
}

func TestWorkflowCallZeroRef(t *testing.T) {
	t.Parallel()

	var ref hook.Ref[Quote, Price]

	// Undeclared: fails before touching the workflow context.
	if _, err := ref.WorkflowCall(nil, Quote{}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("zero ref: %v", err)
	}
}
