package config

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics of the component:
//
//	backplane.config.revisions{outcome=saved|rejected}  saves and rollbacks
//	backplane.config.kv.rewrites{reason=missing|stale|edited}  KV rewritten
//	backplane.config.kv.failures  reconcile passes and deliveries that failed
type metrics struct {
	revisions metric.Int64Counter
	rewrites  metric.Int64Counter
	failures  metric.Int64Counter
}

func newMetrics(m metric.Meter) metrics {
	revisions, _ := m.Int64Counter("backplane.config.revisions",
		metric.WithDescription("override revisions saved or rejected by validation"))
	rewrites, _ := m.Int64Counter("backplane.config.kv.rewrites",
		metric.WithDescription("config/<service>/ rewritten from PostgreSQL, by reason"))
	failures, _ := m.Int64Counter("backplane.config.kv.failures",
		metric.WithDescription("failed deliveries and reconcile passes"))

	return metrics{revisions: revisions, rewrites: rewrites, failures: failures}
}

func (m metrics) revision(ctx context.Context, outcome string) {
	if m.revisions != nil {
		m.revisions.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

func (m metrics) rewrite(ctx context.Context, reason string) {
	if m.rewrites != nil {
		m.rewrites.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	}
}

func (m metrics) failure(ctx context.Context) {
	if m.failures != nil {
		m.failures.Add(ctx, 1)
	}
}

// broadcast signals "something changed" to every subscriber: one pending
// signal each, never a backlog.
type broadcast struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcast() *broadcast { return &broadcast{subs: map[chan struct{}]struct{}{}} }

// subscribe until ctx ends; the channel is closed then.
func (b *broadcast) subscribe(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	})

	return ch
}

func (b *broadcast) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
