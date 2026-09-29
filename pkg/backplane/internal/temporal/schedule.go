package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/internal/wire"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// Memo keys of a schedule the SDK owns. The schedule's own memo carries
// the owning service; the memo of its workflow action carries the
// declaration's fingerprint, which tells whether the schedule is current.
const (
	MemoService     = wire.MemoService
	MemoFingerprint = wire.MemoFingerprint
)

// Schedule is a Temporal Schedule the service declared: it starts Workflow
// with Args on the service's task queue.
type Schedule struct {
	Name     string
	Workflow string // workflow type
	Args     []any
	Spec     client.ScheduleSpec

	Overlap        enumspb.ScheduleOverlapPolicy // unspecified is Temporal's (skip)
	CatchupWindow  time.Duration                 // 0 is Temporal's (one year)
	PauseOnFailure bool
	Paused         bool

	// Of the started workflow; zero is Temporal's default.
	ExecutionTimeout time.Duration
	RunTimeout       time.Duration
	TaskTimeout      time.Duration
	RetryPolicy      *temporal.RetryPolicy
}

// ScheduleID is the id of the service's schedule name; every schedule of
// the service has the prefix "<service>/".
func ScheduleID(service, name string) string { return wire.ScheduleID(service, name) }

// Options is the schedule as Temporal creates it for service: id
// "<service>/<name>", workflow id "<service>/<name>" (Temporal appends the
// start time), the service's task queue, the owner and fingerprint memos.
func (s Schedule) Options(service string) (client.ScheduleOptions, error) {
	sum, err := s.fingerprint(service)
	if err != nil {
		return client.ScheduleOptions{}, err
	}

	id := ScheduleID(service, s.Name)

	return client.ScheduleOptions{
		ID:   id,
		Spec: s.Spec,
		Action: &client.ScheduleWorkflowAction{
			ID:                       id,
			Workflow:                 s.Workflow,
			Args:                     s.Args,
			TaskQueue:                service,
			WorkflowExecutionTimeout: s.ExecutionTimeout,
			WorkflowRunTimeout:       s.RunTimeout,
			WorkflowTaskTimeout:      s.TaskTimeout,
			RetryPolicy:              s.RetryPolicy,
			Memo:                     map[string]any{MemoService: service, MemoFingerprint: sum},
		},
		Overlap:        s.Overlap,
		CatchupWindow:  s.CatchupWindow,
		PauseOnFailure: s.PauseOnFailure,
		Paused:         s.Paused,
		Memo:           map[string]any{MemoService: service},
	}, nil
}

// fingerprint hashes everything the declaration sets; args by their
// encoded payloads, so an argument that cannot be encoded is an error.
func (s Schedule) fingerprint(service string) (string, error) {
	payloads, err := converter.GetDefaultDataConverter().ToPayloads(s.Args...)
	if err != nil {
		return "", fmt.Errorf("schedule %s: args: %w", s.Name, err)
	}

	args, err := proto.MarshalOptions{Deterministic: true}.Marshal(payloads)
	if err != nil {
		return "", fmt.Errorf("schedule %s: args: %w", s.Name, err)
	}

	raw, err := json.Marshal(struct { //nolint:musttag // hashed, never read back
		Service, Workflow      string
		Args                   []byte
		Spec                   client.ScheduleSpec
		Overlap                enumspb.ScheduleOverlapPolicy
		CatchupWindow          time.Duration
		PauseOnFailure, Paused bool
		Execution, Run, Task   time.Duration
		Retry                  *temporal.RetryPolicy
	}{
		service, s.Workflow, args, s.Spec, s.Overlap, s.CatchupWindow, s.PauseOnFailure, s.Paused,
		s.ExecutionTimeout, s.RunTimeout, s.TaskTimeout, s.RetryPolicy,
	})
	if err != nil {
		return "", fmt.Errorf("schedule %s: %w", s.Name, err)
	}

	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:]), nil
}

// ReconcileSchedules brings the service's schedules in Temporal in line
// with the declared ones, in the background once connected: a missing one
// is created, one whose fingerprint differs is replaced, one of the
// service ("<service>/" id and owner memo) no longer declared is deleted.
// A transient failure retries the whole pass with backoff; a schedule
// Temporal rejects as invalid is logged and skipped. Concurrent replicas
// converge: a create that finds the schedule turns into an update.
func (c *Client) ReconcileSchedules(_ context.Context, g node.Group) error {
	declared := make([]Schedule, 0, len(c.p.Env.Schedules()))
	for _, s := range c.p.Env.Schedules() {
		if s, ok := s.(Schedule); ok {
			declared = append(declared, s)
		}
	}

	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ready:
		}

		err := backoff.Retry(ctx, c.retry, func(ctx context.Context) error { return c.reconcile(ctx, declared) },
			func(err error, in time.Duration) {
				c.log.Warn("temporal schedules not reconciled, retrying", xlog.Err(err), xlog.Duration("retry_in", in))
			})
		if err == nil {
			c.log.Debug("temporal schedules reconciled", xlog.Int("declared", len(declared)))
		}

		return nil
	})

	return nil
}

// reconcile makes one pass.
func (c *Client) reconcile(ctx context.Context, declared []Schedule) error {
	conn := c.current()
	if conn == nil {
		return fmt.Errorf("temporal not connected: %w", env.ErrUnavailable)
	}

	schedules := conn.ScheduleClient()
	want := make(map[string]bool, len(declared))

	var errs []error

	for i := range declared {
		s := &declared[i]
		want[ScheduleID(c.p.Service, s.Name)] = true

		err := c.ensure(ctx, schedules, s)

		var invalid *serviceerror.InvalidArgument

		switch {
		case err == nil:
		case errors.As(err, &invalid), errors.Is(err, errDeclaration):
			c.log.Error("temporal schedule rejected, skipped", xlog.String("schedule", s.Name), xlog.Err(err))
		default:
			errs = append(errs, err)
		}
	}

	if err := c.prune(ctx, schedules, want); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

var errDeclaration = errors.New("bad declaration")

// ensure creates s or, when it exists with another fingerprint, replaces
// its spec, action, policies and paused state.
func (c *Client) ensure(ctx context.Context, schedules client.ScheduleClient, s *Schedule) error {
	opts, err := s.Options(c.p.Service)
	if err != nil {
		return fmt.Errorf("%w: %w", errDeclaration, err)
	}

	_, err = schedules.Create(ctx, opts)

	switch {
	case err == nil:
		c.log.Info("temporal schedule created", xlog.String("schedule", opts.ID))

		return nil
	case !errors.Is(err, temporal.ErrScheduleAlreadyRunning):
		return fmt.Errorf("schedule %s: create: %w", opts.ID, err)
	}

	action, _ := opts.Action.(*client.ScheduleWorkflowAction)
	want, _ := action.Memo[MemoFingerprint].(string)
	updated := false

	err = schedules.GetHandle(ctx, opts.ID).Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			if fingerprintOf(in.Description.Schedule.Action) == want {
				return nil, temporal.ErrSkipScheduleUpdate
			}

			updated = true

			return &client.ScheduleUpdate{Schedule: &client.Schedule{
				Action: action,
				Spec:   &opts.Spec,
				Policy: &client.SchedulePolicies{
					Overlap: opts.Overlap, CatchupWindow: opts.CatchupWindow, PauseOnFailure: opts.PauseOnFailure,
				},
				State: &client.ScheduleState{Paused: opts.Paused},
			}}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("schedule %s: update: %w", opts.ID, err)
	}

	if updated {
		c.log.Info("temporal schedule updated", xlog.String("schedule", opts.ID))
	}

	return nil
}

// prune deletes the service's schedules not in want. Only schedules whose
// id has the "<service>/" prefix and whose owner memo is the service are
// considered.
func (c *Client) prune(ctx context.Context, schedules client.ScheduleClient, want map[string]bool) error {
	it, err := schedules.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return fmt.Errorf("schedules: list: %w", err)
	}

	prefix := c.p.Service + "/"

	var errs []error

	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			return fmt.Errorf("schedules: list: %w", err)
		}

		if !strings.HasPrefix(e.ID, prefix) || want[e.ID] || memoString(e.Memo, MemoService) != c.p.Service {
			continue
		}

		err = schedules.GetHandle(ctx, e.ID).Delete(ctx)

		var gone *serviceerror.NotFound

		switch {
		case err == nil:
			c.log.Info("temporal schedule deleted: no longer declared", xlog.String("schedule", e.ID))
		case errors.As(err, &gone):
		default:
			errs = append(errs, fmt.Errorf("schedule %s: delete: %w", e.ID, err))
		}
	}

	return errors.Join(errs...)
}

// fingerprintOf reads the fingerprint memo of a described workflow action.
func fingerprintOf(a client.ScheduleAction) string {
	w, ok := a.(*client.ScheduleWorkflowAction)
	if !ok {
		return ""
	}

	switch v := w.Memo[MemoFingerprint].(type) {
	case string:
		return v
	case *commonpb.Payload:
		return decodeString(v)
	default:
		return ""
	}
}

func memoString(m *commonpb.Memo, key string) string {
	return decodeString(m.GetFields()[key])
}

func decodeString(p *commonpb.Payload) string {
	if p == nil {
		return ""
	}

	var s string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &s); err != nil {
		return ""
	}

	return s
}
