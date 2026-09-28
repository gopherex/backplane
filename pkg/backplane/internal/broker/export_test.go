package broker

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
)

// Fast shortens the timings for tests; maxDeliver bounds deliveries.
func (b *Broker) Fast(maxDeliver int) {
	b.t.reconnectWait = 100 * time.Millisecond
	b.t.reconnectJitter = 50 * time.Millisecond
	b.t.publishTimeout = 2 * time.Second
	b.t.retry = backoff.Policy{Min: 50 * time.Millisecond, Max: 250 * time.Millisecond}
	b.t.maxDeliver = maxDeliver
	b.t.handlerTimeout = 5 * time.Second
	b.t.nak = backoff.Policy{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond}
}

// Event is the metadata of one published event, for BuildHeaders.
type Event = cloudEvent

// BuildHeaders exposes headers.
func BuildHeaders(ctx context.Context, e Event) nats.Header { return headers(ctx, e) }

// DeadHeaders exposes deadHeaders.
func DeadHeaders(orig nats.Header, id, consumer, cause string, delivered uint64) nats.Header {
	return deadHeaders(orig, id, consumer, cause, delivered)
}

// NakDelay exposes nakDelay.
func NakDelay(p backoff.Policy, n uint64) time.Duration { return nakDelay(p, n) }

// Credentials exposes credentials.
func Credentials(content string) error {
	_, err := credentials(content)

	return err
}
