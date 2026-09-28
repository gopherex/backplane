package conformance_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/workflows"
)

const workflowWait = 20 * time.Second

type workflowsConfig struct {
	config.Backplane `json:"backplane"`
}

type workflowsState struct{ scope deps.Scope }

// ticks counts runs of the scheduled workflow across the test's services.
var ticks atomic.Int32

func Greet(ctx workflow.Context, name string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})

	var out string

	err := workflow.ExecuteActivity(ctx, "Hello", name).Get(ctx, &out)

	return out, err
}

func Tick(ctx workflow.Context, what string) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})

	return workflow.ExecuteActivity(ctx, "Count", what).Get(ctx, nil)
}

func registerWorkflows(r worker.Registry) {
	r.RegisterWorkflow(Greet)
	r.RegisterWorkflow(Tick)
	r.RegisterActivityWithOptions(func(_ context.Context, name string) (string, error) { return "Hello, " + name, nil },
		sdkactivity.RegisterOptions{Name: "Hello"})
	r.RegisterActivityWithOptions(func(context.Context, string) error { ticks.Add(1); return nil },
		sdkactivity.RegisterOptions{Name: "Count"})
}

// TestWorkflowsAndSchedules: a service registers its workflows and a
// schedule through the SDK; the client runs a workflow on the service's
// queue; the schedule is created and runs, is updated when its declaration
// changes and deleted when it is no longer declared — across restarts.
func TestWorkflowsAndSchedules(t *testing.T) {
	addr := temporalAddr(t)
	name := uniqueName("wf")
	tc := dialTemporal(t, addr)
	id := name + "/Tick"

	t.Cleanup(func() { _ = tc.ScheduleClient().GetHandle(context.Background(), id).Delete(context.Background()) })

	// 1. Declared: created, runs; the client executes a workflow directly.
	st, _, stop := runWorkflows(t, addr, name, func(root deps.Scope) {
		workflows.Register(root, registerWorkflows)
		workflows.Schedule(root, "Tick", workflows.Every(time.Second), Tick, workflows.Args("tick"))
	})

	wc := workflowClient(t, st.scope)

	run, err := wc.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		ID: name + "/greet", TaskQueue: workflows.Queue(st.scope),
	}, Greet, "conformance")
	if err != nil {
		t.Fatal(err)
	}

	var greeting string
	if err := run.Get(t.Context(), &greeting); err != nil || greeting != "Hello, conformance" {
		t.Fatalf("direct workflow: %q %v", greeting, err)
	}

	waitFor(t, "schedule runs its workflow", func() bool {
		d, ok := describeSchedule(t, tc, id)
		if !ok || len(d.Info.RecentActions) == 0 || ticks.Load() == 0 {
			return false
		}

		res := d.Info.RecentActions[0].StartWorkflowResult

		return res != nil && strings.HasPrefix(res.WorkflowID, id+"-")
	})

	stop()

	// 2. Changed declaration: updated.
	_, _, stop = runWorkflows(t, addr, name, func(root deps.Scope) {
		workflows.Register(root, registerWorkflows)
		workflows.Schedule(root, "Tick", workflows.Every(2*time.Second), Tick, workflows.Args("tick"), workflows.Paused())
	})

	waitFor(t, "schedule updated", func() bool {
		d, ok := describeSchedule(t, tc, id)

		return ok && d.Schedule.State.Paused && len(d.Schedule.Spec.Intervals) == 1 &&
			d.Schedule.Spec.Intervals[0].Every == 2*time.Second
	})

	stop()

	// 3. No longer declared, on a replica without a worker: deleted.
	scheduleListed(t, tc, id)
	t.Setenv("BACKPLANE_TEMPORAL_WORKER_ENABLED", "false")

	_, logs, _ := runWorkflows(t, addr, name, func(root deps.Scope) { workflows.Register(root, registerWorkflows) })

	waitFor(t, "schedule deleted", func() bool { _, ok := describeSchedule(t, tc, id); return !ok })

	if !strings.Contains(logs.String(), "temporal worker disabled") {
		t.Errorf("worker not reported disabled:\n%s", logs)
	}
}

// runWorkflows opens name with Temporal at addr, declares what declare
// does and runs it until stop or the end of the test.
func runWorkflows(
	t *testing.T, addr, name string, declare func(root deps.Scope),
) (*workflowsState, *syncBuffer, func()) {
	t.Helper()

	platform := freePort(t)

	t.Setenv("BACKPLANE_TEMPORAL_ADDR", addr)
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")
	t.Setenv("BACKPLANE_INTERNAL_PORT", platform)
	t.Setenv("BACKPLANE_PUBLIC_PORT", freePort(t))
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "0s")

	logs := &syncBuffer{}

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[workflowsConfig]) (*workflowsState, error) {
		declare(root)

		return &workflowsState{scope: root}, nil
	},
		backplane.Name(name), backplane.Instance(name+"-1"), backplane.Advertise("127.0.0.1"),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(logs))), backplane.ConfigOptions(config.WithoutFile()),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}

		stopped = true

		cancel()

		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}

		if t.Failed() {
			t.Logf("service logs:\n%s", logs)
		}
	}
	t.Cleanup(stop)

	waitFor(t, "service ready", func() bool {
		code, _ := httpGet("http://127.0.0.1:" + platform + "/healthz/readiness")

		return code == http.StatusOK
	})

	return svc.State(), logs, stop
}

// workflowClient waits for the service's Temporal client.
func workflowClient(t *testing.T, scope deps.Scope) client.Client {
	t.Helper()

	var c client.Client

	waitFor(t, "workflows client", func() bool {
		var err error

		c, err = workflows.Client(scope)

		return err == nil
	})

	return c
}

func temporalAddr(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	return addr
}

func dialTemporal(t *testing.T, addr string) client.Client {
	t.Helper()

	tc, err := client.DialContext(t.Context(), client.Options{HostPort: addr, Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler))})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(tc.Close)

	return tc
}

func describeSchedule(t *testing.T, tc client.Client, id string) (*client.ScheduleDescription, bool) {
	t.Helper()

	d, err := tc.ScheduleClient().GetHandle(t.Context(), id).Describe(t.Context())

	var missing *serviceerror.NotFound

	switch {
	case err == nil:
		return d, true
	case errors.As(err, &missing):
		return nil, false
	default:
		t.Fatalf("describe %s: %v", id, err)

		return nil, false
	}
}

// scheduleListed waits until the (eventually consistent) schedule list
// shows id: reconciliation finds what is no longer declared by listing.
func scheduleListed(t *testing.T, tc client.Client, id string) {
	t.Helper()

	waitFor(t, id+" listed", func() bool {
		it, err := tc.ScheduleClient().List(t.Context(), client.ScheduleListOptions{})
		if err != nil {
			return false
		}

		for it.HasNext() {
			if e, err := it.Next(); err == nil && e.ID == id {
				return true
			}
		}

		return false
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(workflowWait)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}
