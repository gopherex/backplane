package temporal_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	inftemporal "github.com/gopherex/backplane/pkg/backplane/infra/temporal"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/temporal"
	"github.com/gopherex/backplane/pkg/backplane/internal/temporal/temporaltest"
)

const scheduleWait = 20 * time.Second

// replica runs one instance of name: connection, worker with register,
// schedule reconciliation of declared. The returned stop ends it early.
func replica(
	t *testing.T, logs *logSink, name, instance string, register func(r worker.Registry), declared ...temporal.Schedule,
) func() {
	t.Helper()

	e := env.New(name, manifest.New(name, "0.0.0"))
	if register != nil {
		e.RegisterWorker(func(r any) {
			if r, ok := r.(worker.Registry); ok {
				register(r)
			}
		})
	}

	for i := range declared {
		e.Schedule(declared[i])
	}

	c := temporal.New(temporal.Params{
		Conn: inftemporal.Config{Addr: temporaltest.Addr(t)}, Service: name, Instance: instance, Log: xlog.NewJSON(xlog.WithWriter(logs)), Env: e,
		Worker: temporal.Tuning{MaxConcurrentActivities: 4, ActivityPollers: 2, WorkflowPollers: 2},
	})
	c.UseFastRetry()

	g := temporaltest.NewGroup(t)

	for _, start := range []func(context.Context, *temporaltest.Group) error{
		func(ctx context.Context, g *temporaltest.Group) error { return c.Connect(ctx, g) },
		func(ctx context.Context, g *temporaltest.Group) error { return c.StartWorker(ctx, g) },
		func(ctx context.Context, g *temporaltest.Group) error { return c.ReconcileSchedules(ctx, g) },
	} {
		if err := start(t.Context(), g); err != nil {
			t.Fatal(err)
		}
	}

	stop := func() {
		g.Stop()

		_ = c.StopWorker(context.Background())
		_ = c.Close(context.Background())
	}
	t.Cleanup(stop)

	return stop
}

// logSink collects the JSON log lines of replicas.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, string(p))

	return len(p), nil
}

// count is how many lines have msg.
func (l *logSink) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := 0

	for _, line := range l.lines {
		if strings.Contains(line, `"msg":"`+msg+`"`) {
			n++
		}
	}

	return n
}

// cleanSchedules deletes every schedule of name at cleanup.
func cleanSchedules(t *testing.T, tc client.Client, name string) {
	t.Helper()

	t.Cleanup(func() {
		ctx := context.Background()

		it, err := tc.ScheduleClient().List(ctx, client.ScheduleListOptions{})
		if err != nil {
			return
		}

		for it.HasNext() {
			e, err := it.Next()
			if err != nil {
				return
			}

			if strings.HasPrefix(e.ID, name+"/") {
				_ = tc.ScheduleClient().GetHandle(ctx, e.ID).Delete(ctx)
			}
		}
	})
}

// eventually polls ok until it holds or the wait ends.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(scheduleWait)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

func describe(t *testing.T, tc client.Client, id string) (*client.ScheduleDescription, bool) {
	t.Helper()

	var missing *serviceerror.NotFound

	// A busy dev server times RPCs out now and then: retry those, fail on
	// anything else.
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		d, err := tc.ScheduleClient().GetHandle(ctx, id).Describe(ctx)

		cancel()

		switch {
		case err == nil:
			return d, true
		case errors.As(err, &missing):
			return nil, false
		case attempt < 5 && transient(err):
			time.Sleep(200 * time.Millisecond)
		default:
			t.Fatalf("describe %s: %v", id, err)

			return nil, false
		}
	}
}

func transient(err error) bool {
	var (
		unavailable *serviceerror.Unavailable
		deadline    *serviceerror.DeadlineExceeded
	)

	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &unavailable) || errors.As(err, &deadline) ||
		status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Unavailable ||
		status.Code(err) == codes.Canceled ||
		strings.Contains(err.Error(), "RST_STREAM") // the dev server resets streams under load
}

// listed waits until the schedule list (eventually consistent) shows id.
func listed(t *testing.T, tc client.Client, id string) {
	t.Helper()

	eventually(t, id+" listed", func() bool {
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

func every(d time.Duration) client.ScheduleSpec {
	return client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: d}}}
}

// TestSchedulesReconcile: two replicas create the declared schedule at
// once and its workflow runs; a changed declaration updates it; a schedule
// of the service no longer declared is deleted, a foreign one under the
// same prefix without the owner memo is not.
func TestSchedulesReconcile(t *testing.T) {
	t.Parallel()

	name := temporaltest.Name("sched")
	tc := temporaltest.Dial(t)
	cleanSchedules(t, tc, name)

	var ticks atomic.Int32

	tick := func(ctx workflow.Context, n int) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})

		return workflow.ExecuteActivity(ctx, "Count", n).Get(ctx, nil)
	}
	register := func(r worker.Registry) {
		r.RegisterWorkflowWithOptions(tick, workflow.RegisterOptions{Name: "Tick"})
		r.RegisterActivityWithOptions(func(context.Context, int) error { ticks.Add(1); return nil },
			activity.RegisterOptions{Name: "Count"})
	}

	id := temporal.ScheduleID(name, "Tick")

	// Left by an earlier version, and one someone else made by hand.
	for _, s := range []client.ScheduleOptions{
		{
			ID: name + "/Stale", Spec: every(time.Hour), Paused: true, Memo: map[string]any{temporal.MemoService: name},
			Action: &client.ScheduleWorkflowAction{Workflow: "Tick", TaskQueue: name},
		},
		{
			ID: name + "/Foreign", Spec: every(time.Hour), Paused: true,
			Action: &client.ScheduleWorkflowAction{Workflow: "Tick", TaskQueue: name},
		},
	} {
		for attempt := 1; ; attempt++ {
			_, err := tc.ScheduleClient().Create(t.Context(), s)
			if err == nil {
				break
			}

			if attempt == 5 || !transient(err) {
				t.Fatal(err)
			}

			time.Sleep(200 * time.Millisecond)
		}

		listed(t, tc, s.ID)
	}

	logs := &logSink{}
	first := temporal.Schedule{Name: "Tick", Workflow: "Tick", Args: []any{1}, Spec: every(time.Second)}
	stopA := replica(t, logs, name, name+"-a", register, first)
	stopB := replica(t, logs, name, name+"-b", register, first)

	eventually(t, "Tick created and run", func() bool {
		_, ok := describe(t, tc, id)

		return ok && ticks.Load() > 0
	})
	eventually(t, "Stale deleted", func() bool { _, ok := describe(t, tc, name+"/Stale"); return !ok })

	if _, ok := describe(t, tc, name+"/Foreign"); !ok {
		t.Error("foreign schedule deleted")
	}

	desc, _ := describe(t, tc, id)
	if a, ok := desc.Schedule.Action.(*client.ScheduleWorkflowAction); !ok || a.TaskQueue != name ||
		!strings.HasPrefix(a.ID, id) {
		t.Errorf("action: %+v", desc.Schedule.Action)
	}

	stopA()
	stopB()

	if created, retried := logs.count("temporal schedule created"), logs.count("temporal schedules not reconciled, retrying"); created != 1 || retried != 0 {
		t.Errorf("two replicas: created %d times, %d retries; want 1 and 0", created, retried)
	}

	// Changed declaration: updated in place.
	second := first
	second.Spec, second.Paused = every(2*time.Second), true
	stop := replica(t, logs, name, name+"-a", register, second)

	eventually(t, "Tick updated", func() bool {
		d, ok := describe(t, tc, id)

		return ok && d.Schedule.State.Paused && len(d.Schedule.Spec.Intervals) == 1 &&
			d.Schedule.Spec.Intervals[0].Every == 2*time.Second
	})

	stop()

	if n := logs.count("temporal schedule updated"); n != 1 {
		t.Errorf("updated %d times, want 1", n)
	}

	// No longer declared: deleted.
	listed(t, tc, id)
	replica(t, logs, name, name+"-a", register)

	eventually(t, "Tick deleted", func() bool { _, ok := describe(t, tc, id); return !ok })

	if n := logs.count("temporal schedule rejected, skipped"); n != 0 {
		t.Errorf("%d schedules rejected", n)
	}
}

// A schedule Temporal rejects is logged and skipped; the others are
// created and the pass is not retried.
func TestScheduleRejectedByTemporal(t *testing.T) {
	t.Parallel()

	name := temporaltest.Name("sched")
	tc := temporaltest.Dial(t)
	cleanSchedules(t, tc, name)

	logs := &logSink{}
	replica(t, logs, name, name+"-a", nil,
		temporal.Schedule{Name: "Bad", Workflow: "Tick", Spec: client.ScheduleSpec{CronExpressions: []string{"not a cron"}}},
		temporal.Schedule{Name: "Good", Workflow: "Tick", Spec: every(time.Hour), Paused: true})

	eventually(t, "Good created", func() bool { _, ok := describe(t, tc, temporal.ScheduleID(name, "Good")); return ok })
	eventually(t, "Bad rejected", func() bool { return logs.count("temporal schedule rejected, skipped") == 1 })

	if _, ok := describe(t, tc, temporal.ScheduleID(name, "Bad")); ok {
		t.Error("Bad created")
	}

	if n := logs.count("temporal schedules not reconciled, retrying"); n != 0 {
		t.Errorf("%d retries", n)
	}
}

func TestWorkerTuning(t *testing.T) {
	t.Parallel()

	c := temporal.New(temporal.Params{Service: "svc", Worker: temporal.Tuning{
		MaxConcurrentActivities: 7, MaxConcurrentWorkflowTasks: 5, ActivityPollers: 3, WorkflowPollers: 2,
	}})

	o := c.WorkerOptions()
	if o.MaxConcurrentActivityExecutionSize != 7 || o.MaxConcurrentWorkflowTaskExecutionSize != 5 ||
		o.MaxConcurrentActivityTaskPollers != 3 || o.MaxConcurrentWorkflowTaskPollers != 2 || o.WorkerStopTimeout == 0 {
		t.Errorf("options: %+v", o)
	}

	if d := temporal.New(temporal.Params{Service: "svc"}).WorkerOptions(); d.MaxConcurrentActivityExecutionSize != 0 ||
		d.MaxConcurrentActivityTaskPollers != 0 {
		t.Errorf("zero tuning is not Temporal's default: %+v", d)
	}
}
