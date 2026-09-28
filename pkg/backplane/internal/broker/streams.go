package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Platform defaults of the streams. The emitter owns its stream's
// configuration; a subscriber creates a missing stream with the same
// defaults and never overwrites an existing one.
const (
	eventsMaxAge = 7 * 24 * time.Hour
	deadMaxAge   = 30 * 24 * time.Hour
	dedupWindow  = 2 * time.Minute

	metaService = "bp.service"
	metaBy      = "bp.ensured-by"
	metaKind    = "bp.kind"
	byEmitter   = "emitter"
	bySubscribe = "subscriber"
	kindEvents  = "events"
	kindDead    = "dead-letters"
)

// ErrNotConnected: NATS is not reachable right now.
var ErrNotConnected = errors.New("broker: nats not connected")

// eventStream is the configuration of service's event stream; owner records
// who ensured it last (emitter or subscriber).
func eventStream(service, owner string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        StreamName(service),
		Description: "backplane events of " + service,
		Subjects:    []string{StreamSubjects(service)},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      eventsMaxAge,
		Duplicates:  dedupWindow,
		Replicas:    1,
		Metadata:    map[string]string{metaService: service, metaBy: owner, metaKind: kindEvents},
	}
}

// deadStream is the configuration of subscriber's dead-letter stream.
func deadStream(subscriber string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        DLQStreamName(subscriber),
		Description: "backplane dead letters of " + subscriber,
		Subjects:    []string{DLQStreamSubjects(subscriber)},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      deadMaxAge,
		Duplicates:  dedupWindow,
		Replicas:    1,
		Metadata:    map[string]string{metaService: subscriber, metaBy: bySubscribe, metaKind: kindDead},
	}
}

// ensureOwn creates or updates the service's own stream: the emitter's
// configuration wins.
func (b *Broker) ensureOwn(ctx context.Context) error {
	jet, err := b.connected()
	if err != nil {
		return err
	}

	if _, err := jet.CreateOrUpdateStream(ctx, eventStream(b.p.Service, byEmitter)); err != nil {
		return fmt.Errorf("broker: stream %s: %w", StreamName(b.p.Service), err)
	}

	return nil
}

// ensureExists creates a missing stream and leaves an existing one alone.
func ensureExists(ctx context.Context, jet jetstream.JetStream, cfg jetstream.StreamConfig) error {
	_, err := jet.Stream(ctx, cfg.Name)
	if err == nil {
		return nil
	}

	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("broker: stream %s: %w", cfg.Name, err)
	}

	if _, err := jet.CreateStream(ctx, cfg); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return fmt.Errorf("broker: create stream %s: %w", cfg.Name, err)
	}

	return nil
}
