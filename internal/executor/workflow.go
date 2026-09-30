package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	otelsdk "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/bindings"
	"github.com/gopherex/backplane/internal/wire"
)

// maxAttempts bounds a step's retry attempts so they convert to int32.
const maxAttempts = 1 << 30

// RegisterWorkflow adds wire.BindingWorkflow to r without metrics: tests
// and tools. backplane's worker takes Executor.RegisterWorkflows.
func RegisterWorkflow(r worker.Registry) { registerWorkflow(r, metrics{}) }

func registerWorkflow(r worker.Registry, m metrics) {
	r.RegisterWorkflowWithOptions(bindingWorkflow(m), workflow.RegisterOptions{Name: wire.BindingWorkflow})
}

// bindingWorkflow is wire.BindingWorkflow: it runs a compiled binding or
// rule; m records runs and steps.
//
// A step starts as soon as every step it depends on is done (ready steps
// by name); a step whose when is false is skipped. A step is its activity
// executed by name on the
// owning service's queue with the ActivityCall envelope (or, for an
// activity of kind WORKFLOW, a child workflow by name there), with the
// step's retry policy and timeouts; its output is ActivityResult.payload.
// When a step fails — after its retries — or an expression fails, the
// steps already done are compensated: each one's undo, in reverse order of
// completion; then the run fails with "step <name>: <service>.<Activity>:
// <message>" (wire.StepFailedType) or "transform failed at ..."
// (wire.TransformFailedType), non-retryable. Canceled, it compensates the
// same way and ends canceled. A binding's result is HookResult.payload; a
// rule's run returns an empty HookResult.
//
// Evaluation is the Program's: pure and deterministic, so replays agree.
func bindingWorkflow(m metrics) func(ctx workflow.Context, in Input) (*backplanev1.HookResult, error) {
	return func(ctx workflow.Context, in Input) (*backplanev1.HookResult, error) {
		start := workflow.Now(ctx)
		r := &runner{m: m, in: in, ctx: ctx}

		out, err := r.run()

		if !workflow.IsReplaying(ctx) {
			outcome := outcomeOK

			switch {
			case temporal.IsCanceledError(err):
				outcome = outcomeCanceled
			case err != nil:
				outcome = outcomeFailed
			}

			m.run(in.Kind, r.source(), outcome, workflow.Now(ctx).Sub(start))
		}

		return out, err
	}
}

// runner is one run.
type runner struct {
	m     metrics
	in    Input
	ctx   workflow.Context
	prog  *bindings.Program
	scope bindings.Scope
	trace map[string]string
	// done are the steps that completed, in completion order: what undo
	// compensates.
	done []string
}

func (r *runner) source() string {
	if r.prog != nil {
		return r.prog.Source()
	}

	if r.in.Identity.Rule != "" {
		return "rule:" + r.in.Identity.Rule
	}

	return r.in.Identity.Hook
}

func (r *runner) run() (*backplanev1.HookResult, error) {
	if err := r.start(); err != nil {
		return nil, err
	}

	r.trace = workflowTrace(r.ctx, r.in.Trace)

	if err := r.steps(); err != nil {
		return nil, r.fail(err)
	}

	if r.prog.Kind() == bindings.KindRule {
		return &backplanev1.HookResult{}, nil
	}

	out, err := r.prog.Result(r.scope)
	if err != nil {
		return nil, r.fail(transform(err))
	}

	return &backplanev1.HookResult{Payload: out}, nil
}

// start reads the program and the input into the first scope.
func (r *runner) start() error {
	prog := new(bindings.Program)
	if err := json.Unmarshal(r.in.Program, prog); err != nil {
		return failure(fmt.Sprintf("transform failed at program: %v", err), wire.TransformFailedType)
	}

	r.prog = prog

	payload := []byte(r.in.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	var err error

	switch {
	case prog.Kind() == bindings.KindRule && r.in.Kind == KindRule:
		r.scope, err = prog.StartEvent(payload, r.in.Meta.meta())
	case prog.Kind() == bindings.KindBinding && r.in.Kind == KindBinding:
		r.scope, err = prog.Start(payload)
	default:
		return failure(fmt.Sprintf("transform failed at program: a %s program in a %s run", kindName(prog.Kind()), r.in.Kind),
			wire.TransformFailedType)
	}

	if err != nil {
		return transform(err)
	}

	return nil
}

func kindName(k bindings.Kind) string {
	if k == bindings.KindRule {
		return string(KindRule)
	}

	return string(KindBinding)
}

// launched is a step in flight.
type launched struct {
	plan  bindings.StepPlan
	fut   workflow.Future
	start time.Time
}

// steps runs every step once its dependencies are settled: the ready ones
// start at once, by name (a skip settles a step at once and may make
// others ready), then the run waits for any call in flight. After a
// failure nothing new starts and the calls in flight are awaited, so every
// step that did its work is known to undo. The error is the first in
// completion order (an expression's before the calls it prevents).
func (r *runner) steps() error {
	plans := r.prog.Steps()
	settled := make(map[string]bool, len(plans))
	started := make(map[string]bool, len(plans))
	sel := workflow.NewSelector(r.ctx)

	var (
		first    error
		inFlight int
	)

	for {
		for ready := first == nil; ready; {
			ready = false

			for i := range plans {
				plan := plans[i]
				if started[plan.Name] || !allSettled(plan.Deps, settled) {
					continue
				}

				started[plan.Name] = true

				l, err := r.launch(plan)
				if err != nil {
					first = err

					break
				}

				if l == nil {
					settled[plan.Name], ready = true, true

					continue
				}

				inFlight++

				sel.AddFuture(l.fut, func(f workflow.Future) {
					inFlight--
					settled[l.plan.Name] = true

					if err := r.settle(*l, f); err != nil && first == nil {
						first = err
					}
				})
			}

			ready = ready && first == nil
		}

		if inFlight == 0 {
			break
		}

		sel.Select(r.ctx)
	}

	if first == nil && len(settled) < len(plans) {
		return failure("transform failed at program: steps wait for each other", wire.TransformFailedType)
	}

	return first
}

func allSettled(deps []string, settled map[string]bool) bool {
	for _, dep := range deps {
		if !settled[dep] {
			return false
		}
	}

	return true
}

// launch starts a step's call; nil when its when skips it.
func (r *runner) launch(plan bindings.StepPlan) (*launched, error) {
	run, err := r.prog.When(plan.Name, r.scope)
	if err != nil {
		return nil, transform(err)
	}

	if !run {
		if r.scope, err = r.prog.Skip(r.scope, plan.Name); err != nil {
			return nil, transform(err)
		}

		return nil, nil //nolint:nilnil // skipped: nothing to await
	}

	input, err := r.prog.Input(plan.Name, r.scope)
	if err != nil {
		return nil, transform(err)
	}

	return &launched{
		plan: plan, fut: r.call(r.ctx, plan, plan.Activity, plan.Service, plan.Kind, input, false),
		start: workflow.Now(r.ctx),
	}, nil
}

// settle takes a finished step's output into the scope.
func (r *runner) settle(l launched, f workflow.Future) error {
	var res backplanev1.ActivityResult

	err := f.Get(r.ctx, &res)
	r.observe(l.plan.Activity, false, err, l.start)

	if err != nil {
		return stepFailure(l.plan.Name, l.plan.Activity, err)
	}

	r.done = append(r.done, l.plan.Name)

	out := res.GetPayload()
	if len(out) == 0 {
		out = []byte("{}")
	}

	scope, err := r.prog.Bind(r.scope, l.plan.Name, out)
	if err != nil {
		return transform(err)
	}

	r.scope = scope

	return nil
}

func (r *runner) observe(activity string, undo bool, err error, start time.Time) {
	if workflow.IsReplaying(r.ctx) {
		return
	}

	outcome := outcomeOK
	if err != nil {
		outcome = outcomeError
	}

	r.m.step(activity, undo, outcome, workflow.Now(r.ctx).Sub(start))
}

// call executes activity full (a step's or its undo) on service's queue:
// an activity, or a child workflow for kind WORKFLOW.
func (r *runner) call(
	ctx workflow.Context, plan bindings.StepPlan, full, service string, kind backplanev1.ActivityKind, input []byte,
	undo bool,
) workflow.Future {
	_, name := bindings.SplitName(full)
	call := &backplanev1.ActivityCall{
		Activity: full, Payload: input, Trace: r.trace, Binding: r.in.Identity.String(), Step: plan.Name,
	}
	retry := &temporal.RetryPolicy{
		InitialInterval: plan.Retry.InitialInterval, BackoffCoefficient: plan.Retry.Backoff,
		MaximumInterval: plan.Retry.MaxInterval,
		MaximumAttempts: int32(min(plan.Retry.Attempts, maxAttempts)), //nolint:gosec // bounded
	}

	if kind == backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
		id := workflow.GetInfo(ctx).WorkflowExecution.ID + "/" + plan.Name
		if undo {
			id += "/undo"
		}

		cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID: id, TaskQueue: wire.Queue(service), WorkflowRunTimeout: plan.StartToClose, RetryPolicy: retry,
		})

		return workflow.ExecuteChildWorkflow(cctx, name, call)
	}

	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: wire.Queue(service), StartToCloseTimeout: plan.StartToClose, HeartbeatTimeout: plan.Heartbeat,
		RetryPolicy: retry,
	})

	return workflow.ExecuteActivity(actx, name, call)
}

// fail compensates the steps done, newest first, and is the run's error:
// cause's message with the failed undos appended. A canceled run ends
// canceled.
func (r *runner) fail(cause error) error {
	canceled := temporal.IsCanceledError(cause) || r.ctx.Err() != nil
	ctx, _ := workflow.NewDisconnectedContext(r.ctx)

	var undone []string

	for i := len(r.done) - 1; i >= 0; i-- {
		plan, ok := r.prog.Step(r.done[i])
		if !ok || plan.Undo == "" {
			continue
		}

		input, err := r.prog.UndoInput(plan.Name, r.scope)
		if err != nil {
			undone = append(undone, "undo "+plan.Name+": "+err.Error())

			continue
		}

		start := workflow.Now(ctx)
		err = r.call(ctx, plan, plan.Undo, plan.UndoService, plan.UndoKind, input, true).Get(ctx, nil)
		r.observe(plan.Undo, true, err, start)

		if err != nil {
			msg, _ := readable(err)
			undone = append(undone, "undo "+plan.Name+": "+prefixed(plan.Undo, msg))
		}
	}

	msg, typ := readableFailure(cause)
	if len(undone) > 0 {
		msg += " (" + strings.Join(undone, "; ") + ")"
	}

	if canceled {
		return temporal.NewCanceledError(msg) //nolint:wrapcheck // the error Temporal carries
	}

	return failure(msg, typ)
}

// failure is a non-retryable application error of type typ.
func failure(msg, typ string) error {
	return temporal.NewNonRetryableApplicationError(msg, typ, nil) //nolint:wrapcheck // the error Temporal carries
}

// transform is an expression's failure on the run's values.
func transform(err error) error {
	var eval *bindings.EvalError
	if errors.As(err, &eval) {
		return failure(eval.Error(), wire.TransformFailedType)
	}

	return failure("transform failed: "+err.Error(), wire.TransformFailedType)
}

// stepFailure is "step <name>: <service>.<Activity>: <message>". The
// SDK already prefixes a handler's message with the activity's name.
// A canceled step stays a cancellation.
func stepFailure(step, activity string, err error) error {
	msg, _ := readable(err)
	msg = "step " + step + ": " + prefixed(activity, msg)

	if temporal.IsCanceledError(err) {
		return temporal.NewCanceledError(msg) //nolint:wrapcheck // the error Temporal carries
	}

	return failure(msg, wire.StepFailedType)
}

func prefixed(activity, msg string) string {
	if strings.HasPrefix(msg, activity+": ") {
		return msg
	}

	return activity + ": " + msg
}

// readableFailure is the message and type of one of the run's own errors.
func readableFailure(err error) (string, string) {
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		return app.Message(), app.Type()
	}

	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		var msg string
		if canceled.HasDetails() {
			_ = canceled.Details(&msg)
		}

		if msg == "" {
			msg = "canceled"
		}

		return msg, ""
	}

	msg, typ := readable(err)

	return msg, typ
}

// workflowTrace is the trace context for the steps' envelopes: the
// workflow's own span (the tracing interceptor's) when it has one, else
// the caller's.
func workflowTrace(ctx workflow.Context, fallback map[string]string) map[string]string {
	span, ok := otelsdk.SpanFromWorkflowContext(ctx)
	if !ok || !span.SpanContext().IsValid() {
		return fallback
	}

	carrier := propagation.MapCarrier{}
	w3c{}.Inject(trace.ContextWithSpanContext(context.Background(), span.SpanContext()), carrier)

	if len(carrier) == 0 {
		return fallback
	}

	return carrier
}

// w3c is the envelopes' trace format: traceparent, tracestate, baggage.
type w3c struct {
	propagation.TraceContext
	propagation.Baggage
}

func (w w3c) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	w.TraceContext.Inject(ctx, carrier)
	w.Baggage.Inject(ctx, carrier)
}

func (w w3c) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return w.Baggage.Extract(w.TraceContext.Extract(ctx, carrier), carrier)
}
