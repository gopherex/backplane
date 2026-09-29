package executor

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics of the executor (on its node's meter):
//
//	backplane.binding.calls{hook, outcome}   Nexus starts: run | default | no_binding | error
//	backplane.binding.runs{kind, source, outcome}   ended runs: ok | failed | canceled
//	backplane.binding.run.duration{kind, source, outcome}   s, a run start to end
//	backplane.binding.step.duration{activity, undo, outcome}   s, one step: ok | error
//	backplane.executor.hook_services   gauge: services the Nexus worker serves
//
// Runs and steps are recorded from workflow code outside replay, so a run
// counts once whichever replica finishes it.
type metrics struct {
	calls    metric.Int64Counter
	runs     metric.Int64Counter
	runTime  metric.Float64Histogram
	stepTime metric.Float64Histogram
	services metric.Int64Gauge
}

// Outcomes.
const (
	outcomeOK        = "ok"
	outcomeFailed    = "failed"
	outcomeCanceled  = "canceled"
	outcomeError     = "error"
	outcomeRun       = "run"
	outcomeDefault   = "default"
	outcomeNoBinding = "no_binding"
)

func newMetrics(m metric.Meter) metrics {
	if m == nil {
		return metrics{}
	}

	// Creation only fails on invalid names, fixed here.
	calls, _ := m.Int64Counter("backplane.binding.calls",
		metric.WithDescription("hook calls handled over Nexus, by outcome"))
	runs, _ := m.Int64Counter("backplane.binding.runs",
		metric.WithDescription("binding and rule runs ended, by outcome"))
	runTime, _ := m.Float64Histogram("backplane.binding.run.duration", metric.WithUnit("s"),
		metric.WithDescription("binding and rule runs, start to end"))
	stepTime, _ := m.Float64Histogram("backplane.binding.step.duration", metric.WithUnit("s"),
		metric.WithDescription("one step of a run: its activity or child workflow with retries"))
	services, _ := m.Int64Gauge("backplane.executor.hook_services",
		metric.WithDescription("services whose hooks the Nexus worker serves"))

	return metrics{calls: calls, runs: runs, runTime: runTime, stepTime: stepTime, services: services}
}

func (m metrics) call(ctx context.Context, hook, outcome string) {
	if m.calls != nil {
		m.calls.Add(ctx, 1, metric.WithAttributes(attribute.String("hook", hook), attribute.String("outcome", outcome)))
	}
}

func (m metrics) run(kind Kind, source, outcome string, took time.Duration) {
	attrs := metric.WithAttributes(attribute.String("kind", string(kind)), attribute.String("source", source),
		attribute.String("outcome", outcome))

	if m.runs != nil {
		m.runs.Add(context.Background(), 1, attrs)
	}

	if m.runTime != nil {
		m.runTime.Record(context.Background(), took.Seconds(), attrs)
	}
}

func (m metrics) step(activity string, undo bool, outcome string, took time.Duration) {
	if m.stepTime != nil {
		m.stepTime.Record(context.Background(), took.Seconds(), metric.WithAttributes(
			attribute.String("activity", activity), attribute.Bool("undo", undo), attribute.String("outcome", outcome)))
	}
}

func (m metrics) hookServices(ctx context.Context, n int) {
	if m.services != nil {
		m.services.Record(ctx, int64(n))
	}
}
