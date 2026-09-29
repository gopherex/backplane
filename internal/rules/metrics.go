package rules

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcome is what became of one delivery of an event to a rule.
type outcome string

// Outcomes of a delivery (backplane.rules.messages{outcome}).
const (
	// outcomeSkipped: `when` is false — acked, no run.
	outcomeSkipped outcome = "skipped"
	// outcomeStarted: a run started — acked.
	outcomeStarted outcome = "started"
	// outcomeDedup: the run of this ce-id exists already — acked.
	outcomeDedup outcome = "dedup"
	// outcomeFailed: the start failed — nak with backoff (or a plain nak
	// when the stop interrupted it).
	outcomeFailed outcome = "failed"
	// outcomeInvalid: no ce-id, a payload that is not JSON, or `when`
	// failed on it — terminated, never retried (rules have no DLQ).
	outcomeInvalid outcome = "invalid"
)

// metrics of the component:
//
//	backplane.rules.messages{rule, outcome=skipped|started|dedup|failed|invalid}
//	    deliveries of events to rules by what became of them
//	backplane.rules.matched{rule}  deliveries whose `when` was true
//	    (= started + dedup + failed)
type metrics struct {
	messages metric.Int64Counter
	matched  metric.Int64Counter
}

func newMetrics(m metric.Meter) metrics {
	messages, _ := m.Int64Counter("backplane.rules.messages",
		metric.WithDescription("deliveries of events to rules, by outcome"))
	matched, _ := m.Int64Counter("backplane.rules.matched",
		metric.WithDescription("deliveries of events whose rule's when was true"))

	return metrics{messages: messages, matched: matched}
}

func (m metrics) record(ctx context.Context, rule string, o outcome) {
	attr := attribute.String("rule", rule)

	if m.messages != nil {
		m.messages.Add(ctx, 1, metric.WithAttributes(attr, attribute.String("outcome", string(o))))
	}

	if m.matched != nil && (o == outcomeStarted || o == outcomeDedup || o == outcomeFailed) {
		m.matched.Add(ctx, 1, metric.WithAttributes(attr))
	}
}
