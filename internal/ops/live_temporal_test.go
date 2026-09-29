package ops_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/codes"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/ops"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/wire"
)

// liveTemporal connects to BACKPLANE_TEST_TEMPORAL; skipped without it.
func liveTemporal(t *testing.T) client.Client {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	c, err := client.DialContext(t.Context(), client.Options{
		HostPort: addr, Namespace: "default", Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(c.Close)

	return c
}

func serve(t *testing.T, c client.Client, queue string, register func(w worker.Worker)) {
	t.Helper()

	w := worker.New(c, queue, worker.Options{})
	register(w)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)
}

type greetIn struct {
	Name string `json:"name"`
}

// The target service: activities Echo and Fail, workflow-backed activity
// Flow, declared workflows Greet and Wait.
func echo(_ context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return &backplanev1.ActivityResult{Payload: call.GetPayload()}, nil
}

func fail(context.Context, *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return nil, temporal.NewNonRetryableApplicationError("svc.Fail: refused", wire.NonRetryableType, nil)
}

func flow(_ workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return &backplanev1.ActivityResult{Payload: []byte(`"flowed ` + call.GetActivity() + `"`)}, nil
}

func greet(_ workflow.Context, in greetIn) (string, error) { return "Hello, " + in.Name, nil }

// wait returns on the "go" signal; a cancellation ends it canceled
// (Receive alone does not see cancellation).
func wait(ctx workflow.Context) (string, error) {
	var v string

	s := workflow.NewSelector(ctx)
	s.AddReceive(workflow.GetSignalChannel(ctx, "go"), func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, &v) })
	s.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
	s.Select(ctx)

	if err := ctx.Err(); err != nil {
		return "", err
	}

	return "got " + v, nil
}

// TestWorkflowsLive: against Temporal, the console starts declared
// workflows, lists and describes runs, signals, cancels and terminates
// them, runs activities by name and raises hooks through Nexus, and
// operates schedules.
//
//nolint:paralleltest // one scenario over one Temporal
func TestWorkflowsLive(t *testing.T) {
	c := liveTemporal(t)
	svc := unique("opst")
	queue := "backplane-" + svc
	ctx := t.Context()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: service(&backplanev1.Manifest{
		Service: svc, Version: "1.0.0",
		Activities: []*backplanev1.Activity{
			{Name: "Echo", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
			{Name: "Fail", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
			{Name: "Flow", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW},
		},
		Workflows: []*backplanev1.Workflow{{Name: "Greet"}, {Name: "Wait"}},
		Hooks:     []*backplanev1.Hook{{Name: "Ask"}},
		Schedules: []*backplanev1.Schedule{{Name: "Nightly", Workflow: "Greet"}},
	})})

	o := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return c, nil }, queue))
	srv := newServers(o)

	serve(t, c, queue, func(w worker.Worker) { ops.RegisterWorkflows(w) })
	serve(t, c, svc, func(w worker.Worker) {
		w.RegisterActivityWithOptions(echo, activity.RegisterOptions{Name: "Echo"})
		w.RegisterActivityWithOptions(fail, activity.RegisterOptions{Name: "Fail"})
		w.RegisterWorkflowWithOptions(flow, workflow.RegisterOptions{Name: "Flow"})
		w.RegisterWorkflowWithOptions(greet, workflow.RegisterOptions{Name: "Greet"})
		w.RegisterWorkflowWithOptions(wait, workflow.RegisterOptions{Name: "Wait"})
	})

	// Declared workflow: start, result, listed.
	started, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{
		Service: svc, Workflow: "Greet", Input: `{"name":"console"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	run := waitRun(t, srv, started.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if run.GetResult() != `"Hello, console"` || run.GetInput() != `{"name":"console"}` ||
		run.GetRun().GetMemo()[wire.MemoSource] != `"admin"` || len(run.GetHistory()) == 0 {
		t.Fatalf("run: %v", run)
	}

	waitFor(t, "run listed", func() bool {
		runs, err := srv.workflows.ListRuns(ctx, &consolev1.ListRunsRequest{Service: svc, Workflow: "Greet"})

		return err == nil && len(runs.GetRuns()) == 1 && runs.GetRuns()[0].GetWorkflowId() == started.GetWorkflowId()
	})

	// Signal, cancel, terminate.
	signaled, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := srv.workflows.SignalRun(ctx, &consolev1.SignalRunRequest{
		WorkflowId: signaled.GetWorkflowId(), Signal: "go", Input: `"now"`,
	}); err != nil {
		t.Fatal(err)
	}

	if r := waitRun(t, srv, signaled.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED); r.GetResult() != `"got now"` {
		t.Fatalf("signaled: %v", r)
	}

	for action, want := range map[string]consolev1.RunStatus{
		"cancel": consolev1.RunStatus_RUN_STATUS_CANCELED, "terminate": consolev1.RunStatus_RUN_STATUS_TERMINATED,
	} {
		w, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait"})
		if err != nil {
			t.Fatal(err)
		}

		if action == "cancel" {
			_, err = srv.workflows.CancelRun(ctx, &consolev1.CancelRunRequest{WorkflowId: w.GetWorkflowId()})
		} else {
			_, err = srv.workflows.TerminateRun(ctx, &consolev1.TerminateRunRequest{WorkflowId: w.GetWorkflowId(), Reason: "test"})
		}

		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}

		waitRun(t, srv, w.GetWorkflowId(), want)
	}

	if _, err := srv.workflows.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: "nope-" + svc}); code(t, err) != codes.NotFound {
		t.Fatalf("missing run: %v", err)
	}

	// Activities by name.
	for activityName, want := range map[string]string{"Echo": `{"a":1}`, "Flow": `"flowed ` + svc + `.Flow"`} {
		res, err := srv.calls.RunActivity(ctx, &consolev1.RunActivityRequest{Activity: svc + "." + activityName, Input: `{"a":1}`})
		if err != nil || res.GetResult().GetOutput() != want || res.GetResult().GetError() != "" {
			t.Fatalf("%s: %v %v", activityName, res, err)
		}
	}

	failed, err := srv.calls.RunActivity(ctx, &consolev1.RunActivityRequest{Activity: svc + ".Fail"})
	if err != nil || failed.GetResult().GetError() != "svc.Fail: refused" ||
		failed.GetResult().GetErrorType() != wire.NonRetryableType {
		t.Fatalf("failing activity: %v %v", failed, err)
	}

	// Hooks: no endpoint yet, then through a Nexus endpoint played here.
	if _, err := srv.calls.CallHook(ctx, &consolev1.CallHookRequest{Hook: svc + ".Ask"}); code(t, err) != codes.FailedPrecondition {
		t.Fatalf("no endpoint: %v", err)
	}

	playHooks(t, c, svc)

	asked, err := srv.calls.CallHook(ctx, &consolev1.CallHookRequest{Hook: svc + ".Ask", Input: `{"q":1}`, Key: "k1"})
	if err != nil || asked.GetResult().GetOutput() != `{"answer":{"q":1}}` ||
		asked.GetResult().GetWorkflowId() != "hook/"+svc+"/Ask/k1" {
		t.Fatalf("hook: %v %v", asked, err)
	}

	// Schedules: a declared one and one nobody declares.
	for _, name := range []string{"Nightly", "Old"} {
		if _, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
			ID:     wire.ScheduleID(svc, name),
			Spec:   client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}},
			Action: &client.ScheduleWorkflowAction{ID: svc + "/" + name, Workflow: "Greet", TaskQueue: svc, Args: []any{greetIn{Name: name}}},
			Memo:   map[string]any{wire.MemoService: svc},
		}); err != nil {
			t.Fatal(err)
		}

		id := wire.ScheduleID(svc, name)

		t.Cleanup(func() { _ = c.ScheduleClient().GetHandle(context.Background(), id).Delete(context.Background()) })
	}

	var schedules []*consolev1.ScheduleInfo

	waitFor(t, "schedules listed", func() bool {
		res, err := srv.schedules.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc})
		schedules = res.GetSchedules()

		return err == nil && len(schedules) == 2 && schedules[1].GetState() != nil
	})

	if n := schedules[0]; n.GetName() != "Nightly" || n.GetDeclared() == nil || n.GetState().GetOwner() != svc ||
		n.GetState().GetWorkflowType() != "Greet" || len(n.GetState().GetNextActions()) == 0 {
		t.Fatalf("declared schedule: %v", n)
	}

	if old := schedules[1]; old.GetName() != "Old" || old.GetDeclared() != nil {
		t.Fatalf("undeclared schedule: %v", old)
	}

	if _, err := srv.schedules.PauseSchedule(ctx, &consolev1.PauseScheduleRequest{Service: svc, Name: "Nightly", Note: "maintenance"}); err != nil {
		t.Fatal(err)
	}

	desc, err := c.ScheduleClient().GetHandle(ctx, wire.ScheduleID(svc, "Nightly")).Describe(ctx)
	if err != nil || !desc.Schedule.State.Paused || !strings.HasPrefix(desc.Schedule.State.Note, "maintenance (") {
		t.Fatalf("paused: %v %v", desc, err)
	}

	if _, err := srv.schedules.UnpauseSchedule(ctx, &consolev1.UnpauseScheduleRequest{Service: svc, Name: "Nightly"}); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.schedules.TriggerSchedule(ctx, &consolev1.TriggerScheduleRequest{Service: svc, Name: "Nightly"}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "triggered run", func() bool {
		d, err := c.ScheduleClient().GetHandle(ctx, wire.ScheduleID(svc, "Nightly")).Describe(ctx)

		return err == nil && !d.Schedule.State.Paused && len(d.Info.RecentActions) > 0
	})
}

// waitRun waits until the run reaches status and returns it.
func waitRun(t *testing.T, srv servers, id string, want consolev1.RunStatus) *consolev1.GetRunResponse {
	t.Helper()

	var run *consolev1.GetRunResponse

	waitFor(t, "run "+id+" "+want.String(), func() bool {
		r, err := srv.workflows.GetRun(t.Context(), &consolev1.GetRunRequest{WorkflowId: id})
		run = r

		return err == nil && r.GetRun().GetStatus() == want
	})

	return run
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// playHooks plays backplane's Nexus side for svc: endpoint svc on a test
// queue serving svc.Hooks with Ask, which answers its input.
func playHooks(t *testing.T, c client.Client, svc string) {
	t.Helper()

	hooks := nexus.NewService(wire.NexusService(svc))

	ask := nexus.NewSyncOperation("Ask", func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		return &backplanev1.HookResult{Payload: []byte(`{"answer":` + string(call.GetPayload()) + `}`)}, nil
	})
	if err := hooks.Register(ask); err != nil {
		t.Fatal(err)
	}

	queue := "nexus-" + svc
	serve(t, c, queue, func(w worker.Worker) { w.RegisterNexusService(hooks) })

	res, err := c.OperatorService().CreateNexusEndpoint(t.Context(), &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: svc,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: "default", TaskQueue: queue},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = c.OperatorService().DeleteNexusEndpoint(context.Background(), &operatorservice.DeleteNexusEndpointRequest{
			Id: res.GetEndpoint().GetId(), Version: res.GetEndpoint().GetVersion(),
		})
	})
}
