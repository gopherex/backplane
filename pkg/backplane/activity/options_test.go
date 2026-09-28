package activity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/activity"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
)

func TestOptionsRecorded(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	activity.Handle(h.Root(), "Charge", charge,
		activity.StartToClose(10*time.Second), activity.HeartbeatTimeout(2*time.Second),
		activity.Retry(activity.RetryHint{Attempts: 5}), activity.Describe("charges an order"))
	activity.Handle(h.Root(), "Plain", charge, activity.StartToClose(0))
	activity.Workflow(h.Root(), "Ship", func(workflow.Context, Order) (Receipt, error) { return Receipt{}, nil },
		activity.Describe("ships"))

	acts := h.Manifest().GetActivities()
	if len(acts) != 3 {
		t.Fatalf("activities: %v", acts)
	}

	c, plain, ship := acts[0], acts[1], acts[2]
	if c.GetKind() != backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY || c.GetStartToClose().AsDuration() != 10*time.Second ||
		c.GetHeartbeat().AsDuration() != 2*time.Second || c.GetRetry().GetAttempts() != 5 ||
		c.GetDescription() != "charges an order" {
		t.Fatalf("charge: %v", c)
	}

	if plain.GetStartToClose() != nil || plain.GetHeartbeat() != nil || plain.GetRetry() != nil {
		t.Fatalf("plain: %v", plain)
	}

	if ship.GetKind() != backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW || ship.GetDescription() != "ships" ||
		ship.GetInput() == nil || ship.GetOutput() == nil {
		t.Fatalf("ship: %v", ship)
	}
}

func TestInfoOf(t *testing.T) {
	t.Parallel()

	if _, ok := activity.InfoOf(t.Context()); ok {
		t.Fatal("info outside a run")
	}

	activity.Heartbeat(t.Context(), "nothing happens")

	var beats []any

	deadline := time.Now().Add(time.Minute)
	ctx := env.WithActivityInfo(t.Context(), env.ActivityInfo{
		Attempt: 2, Binding: "iam.SendEmail", Step: "send", Key: "wf/1", Deadline: deadline,
		Heartbeat: func(d ...any) { beats = append(beats, d...) },
	})

	info, ok := activity.InfoOf(ctx)
	if !ok || info != (activity.Info{Attempt: 2, Binding: "iam.SendEmail", Step: "send", Key: "wf/1", Deadline: deadline}) {
		t.Fatalf("info: %+v %v", info, ok)
	}

	activity.Heartbeat(ctx, 1, "two")

	if len(beats) != 2 {
		t.Fatalf("heartbeat: %v", beats)
	}
}

// Through backplanetest a handler runs without transport info.
func TestInfoOfInHarness(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	activity.Handle(h.Root(), "Probe", func(ctx context.Context, _ Order) (Receipt, error) {
		if _, ok := activity.InfoOf(ctx); ok {
			return Receipt{}, errEmpty
		}

		activity.Heartbeat(ctx)

		return Receipt{Total: 1}, nil
	})
	h.Start()

	if out, err := backplanetest.Activity[Order, Receipt](t.Context(), h, "Probe", Order{}); err != nil || out.Total != 1 {
		t.Fatalf("probe: %v %v", out, err)
	}
}

func TestBadNamePanics(t *testing.T) {
	t.Parallel()

	for _, declare := range []func(){
		func() { activity.Handle(backplanetest.New(t).Root(), "charge", charge) },
		func() {
			activity.Workflow(backplanetest.New(t).Root(), "ship-it",
				func(workflow.Context, Order) (Receipt, error) { return Receipt{}, nil })
		},
	} {
		func() {
			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, "is not CamelCase") {
					t.Errorf("panic %q", r)
				}
			}()

			declare()
		}()
	}
}
