// Package backoff is the SDK's one retry policy: exponential with jitter,
// so instances recovering from the same outage do not retry in lockstep.
package backoff

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	factor = 2
	jitter = 0.2 // ±20%
)

// ErrPolicy: a policy that would spin or never grow.
var ErrPolicy = errors.New("backoff: want 0 < min <= max")

// Policy bounds the delay between attempts.
type Policy struct {
	Min, Max time.Duration
}

// Validate rejects a policy that would spin or never grow.
func (p Policy) Validate() error {
	if p.Min <= 0 || p.Max < p.Min {
		return fmt.Errorf("%w, got %v..%v", ErrPolicy, p.Min, p.Max)
	}

	return nil
}

// Next is the delay after prev (0 for the first retry), jittered.
func (p Policy) Next(prev time.Duration) time.Duration {
	next := p.Min
	if prev > 0 {
		next = min(time.Duration(float64(prev)*factor), p.Max)
	}

	return time.Duration(float64(next) * (1 - jitter + 2*jitter*rand.Float64())) //nolint:gosec // jitter, not crypto
}

// Retry calls fn until it succeeds or ctx ends; onRetry sees every failure
// with the delay before the next attempt.
func Retry(
	ctx context.Context, p Policy, fn func(ctx context.Context) error, onRetry func(err error, in time.Duration),
) error {
	var delay time.Duration

	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		delay = p.Next(delay)
		onRetry(err, delay)

		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()

			return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
		case <-t.C:
		}
	}
}
