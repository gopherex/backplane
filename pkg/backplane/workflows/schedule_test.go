package workflows_test

import (
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	internal "github.com/gopherex/backplane/pkg/backplane/internal/temporal"
	"github.com/gopherex/backplane/pkg/backplane/workflows"
)

type report struct {
	Kind string `json:"kind"`
}

func Nightly(workflow.Context, report) error { return nil }

func notAWorkflow(string) error { return nil }

func TestScheduleRecordsManifest(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	workflows.Schedule(h.Root(), "NightlyReport", workflows.Cron(" 0 3 * * * "), Nightly,
		workflows.TimeZone("Europe/Moscow"), workflows.Overlap(workflows.OverlapBufferOne),
		workflows.Paused(), workflows.Jitter(time.Minute), workflows.Args(report{Kind: "daily"}))
	workflows.Schedule(h.Root(), "Poll", workflows.Every(time.Hour), "custom.Poll")

	got := h.Manifest().GetSchedules()
	if len(got) != 2 {
		t.Fatalf("schedules: %v", got)
	}

	cron, every := got[0], got[1]
	if cron.GetName() != "NightlyReport" || cron.GetCron() != "0 3 * * *" || cron.GetWorkflow() != "Nightly" ||
		cron.GetOverlap() != backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_BUFFER_ONE || !cron.GetPaused() ||
		cron.GetTimeZone() != "Europe/Moscow" || cron.GetJitter().AsDuration() != time.Minute {
		t.Errorf("cron schedule: %v", cron)
	}

	if every.GetName() != "Poll" || every.GetEvery().AsDuration() != time.Hour || every.GetWorkflow() != "custom.Poll" ||
		every.GetOverlap() != backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_SKIP || every.GetPaused() {
		t.Errorf("interval schedule: %v", every)
	}
}

// The declaration as Temporal gets it: ids, queue, action, policies, memos.
func TestScheduleOptions(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	retry := temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3}
	workflows.Schedule(h.Root(), "Nightly", workflows.Every(time.Hour), Nightly,
		workflows.Args(report{Kind: "hourly"}), workflows.Overlap(workflows.OverlapTerminateOther),
		workflows.CatchupWindow(10*time.Minute), workflows.PauseOnFailure(),
		workflows.Timeout(time.Hour), workflows.RunTimeout(30*time.Minute), workflows.Retry(retry))

	declared, err := workflows.Declared(h.Root())
	if err != nil || len(declared) != 1 {
		t.Fatalf("declared: %v %v", declared, err)
	}

	opts, err := declared[0].Options("billing")
	if err != nil {
		t.Fatal(err)
	}

	if opts.ID != "billing/Nightly" || opts.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_TERMINATE_OTHER ||
		opts.CatchupWindow != 10*time.Minute || !opts.PauseOnFailure || opts.Paused ||
		opts.Memo[internal.MemoService] != "billing" {
		t.Errorf("options: %+v", opts)
	}

	if len(opts.Spec.Intervals) != 1 || opts.Spec.Intervals[0].Every != time.Hour || len(opts.Spec.CronExpressions) != 0 {
		t.Errorf("spec: %+v", opts.Spec)
	}

	a, ok := opts.Action.(*client.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("action %T", opts.Action)
	}

	if a.ID != "billing/Nightly" || a.Workflow != "Nightly" || a.TaskQueue != "billing" ||
		a.WorkflowExecutionTimeout != time.Hour || a.WorkflowRunTimeout != 30*time.Minute ||
		a.RetryPolicy == nil || a.RetryPolicy.MaximumAttempts != 3 ||
		len(a.Args) != 1 || a.Args[0] != (report{Kind: "hourly"}) ||
		a.Memo[internal.MemoService] != "billing" || a.Memo[internal.MemoFingerprint] == "" {
		t.Errorf("action: %+v", a)
	}
}

// The fingerprint is stable for one declaration and changes with any
// part of it.
func TestScheduleFingerprint(t *testing.T) {
	t.Parallel()

	fingerprint := func(spec workflows.Spec, opts ...workflows.ScheduleOption) any {
		h := backplanetest.New(t)
		workflows.Schedule(h.Root(), "S", spec, Nightly, opts...)

		declared, err := workflows.Declared(h.Root())
		if err != nil {
			t.Fatal(err)
		}

		o, err := declared[0].Options("svc")
		if err != nil {
			t.Fatal(err)
		}

		a, _ := o.Action.(*client.ScheduleWorkflowAction)

		return a.Memo[internal.MemoFingerprint]
	}

	base := fingerprint(workflows.Every(time.Hour), workflows.Args(report{Kind: "a"}))
	if again := fingerprint(workflows.Every(time.Hour), workflows.Args(report{Kind: "a"})); again != base {
		t.Errorf("unstable: %v vs %v", again, base)
	}

	for name, changed := range map[string]any{
		"spec":    fingerprint(workflows.Every(2*time.Hour), workflows.Args(report{Kind: "a"})),
		"args":    fingerprint(workflows.Every(time.Hour), workflows.Args(report{Kind: "b"})),
		"paused":  fingerprint(workflows.Every(time.Hour), workflows.Args(report{Kind: "a"}), workflows.Paused()),
		"overlap": fingerprint(workflows.Every(time.Hour), workflows.Args(report{Kind: "a"}), workflows.Overlap(workflows.OverlapAllowAll)),
	} {
		if changed == base {
			t.Errorf("%s change kept the fingerprint", name)
		}
	}
}

func TestScheduleRejected(t *testing.T) {
	t.Parallel()

	cases := map[string]func(h *backplanetest.Harness){
		"name with dash": func(h *backplanetest.Harness) {
			workflows.Schedule(h.Root(), "nightly-report", workflows.Every(time.Hour), Nightly)
		},
		"empty name": func(h *backplanetest.Harness) { workflows.Schedule(h.Root(), "", workflows.Every(time.Hour), Nightly) },
		"no spec":    func(h *backplanetest.Harness) { workflows.Schedule(h.Root(), "S", workflows.Spec{}, Nightly) },
		"empty cron": func(h *backplanetest.Harness) { workflows.Schedule(h.Root(), "S", workflows.Cron(" "), Nightly) },
		"sub-second": func(h *backplanetest.Harness) {
			workflows.Schedule(h.Root(), "S", workflows.Every(time.Millisecond), Nightly)
		},
		"not a workflow": func(h *backplanetest.Harness) {
			workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), notAWorkflow)
		},
		"empty type": func(h *backplanetest.Harness) { workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), "") },
		"nil":        func(h *backplanetest.Harness) { workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), nil) },
		"bad overlap": func(h *backplanetest.Harness) {
			workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), Nightly, workflows.Overlap(99))
		},
		"bad args": func(h *backplanetest.Harness) {
			workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), Nightly, workflows.Args(make(chan int)))
		},
	}

	for name, declare := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := backplanetest.New(t)
			declare(h)

			declared, err := workflows.Declared(h.Root())
			if !errors.Is(err, workflows.ErrSchedule) || len(declared) != 0 {
				t.Fatalf("want ErrSchedule and nothing declared, got %v, %v", err, declared)
			}
		})
	}
}

func TestScheduleDuplicate(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), Nightly)
	workflows.Schedule(h.Root(), "S", workflows.Cron("@daily"), Nightly)

	if _, err := workflows.Declared(h.Root()); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestScheduleAfterRunPanics(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	workflows.Seal(h.Root())

	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()

	workflows.Schedule(h.Root(), "S", workflows.Every(time.Hour), Nightly)
}

func TestSpecString(t *testing.T) {
	t.Parallel()

	for spec, want := range map[workflows.Spec]string{
		workflows.Cron("0 3 * * *"):  "cron 0 3 * * *",
		workflows.Every(time.Minute): "every 1m0s",
		{}:                           "no spec",
	} {
		if got := spec.String(); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
