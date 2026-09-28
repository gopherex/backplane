package workflows

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	internal "github.com/gopherex/backplane/pkg/backplane/internal/temporal"
)

// ErrSchedule: a schedule declaration Run refuses.
var ErrSchedule = errors.New("workflows: bad schedule")

var (
	errNoSpec   = errors.New("no spec: use Cron or Every")
	errInterval = errors.New("interval under a second")
	errName     = errors.New("name must be [A-Za-z][A-Za-z0-9_]*")
	errOverlap  = errors.New("unknown overlap policy")
	errWorkflow = errors.New("workflow must be a registered type name or a func(workflow.Context, ...)")
)

// Spec is when a schedule starts its workflow: Cron or Every.
type Spec struct {
	cron  string
	every time.Duration
}

// Cron is a cron expression as Temporal reads it: 5 fields (minute, hour,
// day of month, month of year, day of week), 6 (plus year) or 7 (seconds
// first), or @hourly, @daily, @weekly, @monthly, @yearly. It is read in the
// schedule's time zone (TimeZone, UTC by default).
func Cron(expr string) Spec { return Spec{cron: strings.TrimSpace(expr)} }

// Every is a fixed interval counted from the Unix epoch: Every(time.Hour)
// starts at the top of every hour.
func Every(d time.Duration) Spec { return Spec{every: d} }

// IsZero reports a spec made by neither Cron nor Every.
func (s Spec) IsZero() bool { return s.cron == "" && s.every == 0 }

// String is "cron <expr>" or "every <duration>".
func (s Spec) String() string {
	switch {
	case s.cron != "":
		return "cron " + s.cron
	case s.every != 0:
		return "every " + s.every.String()
	default:
		return "no spec"
	}
}

func (s Spec) validate() error {
	switch {
	case s.IsZero():
		return errNoSpec
	case s.cron == "" && s.every < time.Second:
		return fmt.Errorf("%w: %v", errInterval, s.every)
	}

	return nil
}

// OverlapPolicy is what a start does while the previous run of the
// schedule is still going.
type OverlapPolicy int

// Overlap policies; the zero value is OverlapSkip.
const (
	// OverlapSkip does not start.
	OverlapSkip OverlapPolicy = iota
	// OverlapBufferOne starts once the running one ends; at most one waits.
	OverlapBufferOne
	// OverlapBufferAll starts every missed one after the running one ends.
	OverlapBufferAll
	// OverlapCancelOther cancels the running one, then starts.
	OverlapCancelOther
	// OverlapTerminateOther terminates the running one, then starts.
	OverlapTerminateOther
	// OverlapAllowAll starts regardless.
	OverlapAllowAll
)

func (p OverlapPolicy) temporal() enumspb.ScheduleOverlapPolicy {
	switch p {
	case OverlapBufferOne:
		return enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE
	case OverlapBufferAll:
		return enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ALL
	case OverlapCancelOther:
		return enumspb.SCHEDULE_OVERLAP_POLICY_CANCEL_OTHER
	case OverlapTerminateOther:
		return enumspb.SCHEDULE_OVERLAP_POLICY_TERMINATE_OTHER
	case OverlapAllowAll:
		return enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL
	default:
		return enumspb.SCHEDULE_OVERLAP_POLICY_SKIP
	}
}

func (p OverlapPolicy) manifest() backplanev1.ScheduleOverlap {
	switch p {
	case OverlapBufferOne:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_BUFFER_ONE
	case OverlapBufferAll:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_BUFFER_ALL
	case OverlapCancelOther:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_CANCEL_OTHER
	case OverlapTerminateOther:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_TERMINATE_OTHER
	case OverlapAllowAll:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_ALLOW_ALL
	default:
		return backplanev1.ScheduleOverlap_SCHEDULE_OVERLAP_SKIP
	}
}

func (p OverlapPolicy) valid() bool { return p >= OverlapSkip && p <= OverlapAllowAll }

// ScheduleOption tunes a schedule.
type ScheduleOption func(s *settings)

// settings are what the options set: the schedule and, apart, the
// overlap policy as declared.
type settings struct {
	internal.Schedule

	overlap OverlapPolicy
}

// Args are the arguments of every started workflow, encoded by Temporal's
// default converter (JSON; protojson for proto messages).
func Args(args ...any) ScheduleOption {
	return func(s *settings) { s.Args = args }
}

// Overlap sets what a start does while the previous run still goes
// (OverlapSkip by default).
func Overlap(p OverlapPolicy) ScheduleOption {
	return func(s *settings) { s.overlap = p }
}

// CatchupWindow is how late a start missed while Temporal was unavailable
// may still happen (Temporal's default: one year).
func CatchupWindow(d time.Duration) ScheduleOption {
	return func(s *settings) { s.CatchupWindow = d }
}

// Jitter delays every start by a random amount up to d.
func Jitter(d time.Duration) ScheduleOption {
	return func(s *settings) { s.Spec.Jitter = d }
}

// TimeZone is the IANA zone ("Europe/Moscow") a cron spec is read in; UTC
// by default.
func TimeZone(name string) ScheduleOption {
	return func(s *settings) { s.Spec.TimeZoneName = name }
}

// Paused creates the schedule paused: it starts nothing until unpaused in
// Temporal. An unpause survives restarts until the declaration changes.
func Paused() ScheduleOption {
	return func(s *settings) { s.Paused = true }
}

// PauseOnFailure pauses the schedule when a started workflow fails or
// times out.
func PauseOnFailure() ScheduleOption {
	return func(s *settings) { s.PauseOnFailure = true }
}

// Timeout bounds a started workflow as a whole, retries and
// continue-as-new included (WorkflowExecutionTimeout).
func Timeout(d time.Duration) ScheduleOption {
	return func(s *settings) { s.ExecutionTimeout = d }
}

// RunTimeout bounds one run of a started workflow (WorkflowRunTimeout).
func RunTimeout(d time.Duration) ScheduleOption {
	return func(s *settings) { s.RunTimeout = d }
}

// Retry is the retry policy of a started workflow; without it a failed
// workflow is not retried.
func Retry(p temporal.RetryPolicy) ScheduleOption {
	return func(s *settings) { s.RetryPolicy = &p }
}

// Schedule declares a Temporal Schedule of the service: at every time of
// spec it starts workflow on the service's task queue. Schedule id is
// "<service>/<name>", workflow ids "<service>/<name>-<start time>". name
// is unique within the service: [A-Za-z][A-Za-z0-9_]*.
//
// workflowFn is the workflow function or its registered type name; it must
// be registered on the service's worker with Register — Schedule does not
// register it. Declare before Run, like any declaration.
//
// The SDK reconciles schedules at start, in the background: a missing one
// is created, a changed one updated, one of the service no longer declared
// deleted. Without Temporal configured the declaration stays in the
// manifest and nothing is created.
//
//	workflows.Schedule(root, "NightlyReport", workflows.Cron("0 3 * * *"), reports.Nightly,
//	    workflows.TimeZone("Europe/Moscow"), workflows.Overlap(workflows.OverlapBufferOne))
func Schedule(scope deps.Scope, name string, spec Spec, workflowFn any, opts ...ScheduleOption) {
	e, _ := decl.Env(scope, "schedule "+name)

	o := settings{Schedule: internal.Schedule{Name: name, Spec: specOf(spec)}}
	for _, opt := range opts {
		opt(&o)
	}

	typ, err := workflowType(workflowFn)
	if err == nil {
		err = validate(name, spec, o.overlap)
	}

	if err != nil {
		e.Manifest.Fail(fmt.Errorf("%w %q: %w", ErrSchedule, name, err))

		return
	}

	s := o.Schedule
	s.Workflow, s.Overlap = typ, o.overlap.temporal()

	if _, err := s.Options(e.Service); err != nil {
		e.Manifest.Fail(fmt.Errorf("%w %q: %w", ErrSchedule, name, err))

		return
	}

	e.Manifest.Schedule(record(s, spec, o.overlap))
	e.Schedule(s)
}

func specOf(spec Spec) client.ScheduleSpec {
	if spec.cron != "" {
		return client.ScheduleSpec{CronExpressions: []string{spec.cron}}
	}

	return client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: spec.every}}}
}

func record(s internal.Schedule, spec Spec, overlap OverlapPolicy) *backplanev1.Schedule {
	m := &backplanev1.Schedule{
		Name: s.Name, Workflow: s.Workflow, Overlap: overlap.manifest(), Paused: s.Paused,
		TimeZone: s.Spec.TimeZoneName,
	}

	if spec.cron != "" {
		m.Spec = &backplanev1.Schedule_Cron{Cron: spec.cron}
	} else {
		m.Spec = &backplanev1.Schedule_Every{Every: durationpb.New(spec.every)}
	}

	if s.Spec.Jitter > 0 {
		m.Jitter = durationpb.New(s.Spec.Jitter)
	}

	return m
}

func validate(name string, spec Spec, overlap OverlapPolicy) error {
	if !validName(name) {
		return errName
	}

	if !overlap.valid() {
		return fmt.Errorf("%w %d", errOverlap, overlap)
	}

	return spec.validate()
}

func validName(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_'):
		default:
			return false
		}
	}

	return name != ""
}

// workflowType is the type name Temporal registers fn under by default: a
// string as is, a function by its short name (as worker.RegisterWorkflow
// names it).
func workflowType(fn any) (string, error) {
	if name, ok := fn.(string); ok {
		if name == "" {
			return "", errWorkflow
		}

		return name, nil
	}

	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() || v.Type().NumIn() == 0 ||
		v.Type().In(0) != reflect.TypeFor[workflow.Context]() {
		return "", fmt.Errorf("%w, got %T", errWorkflow, fn)
	}

	full := runtime.FuncForPC(v.Pointer()).Name()

	return strings.TrimSuffix(full[strings.LastIndex(full, ".")+1:], "-fm"), nil
}
