// Package backoff is the SDK's retry policy as a value: bounds the author
// can set, run by github.com/cenkalti/backoff (exponential, jittered, so
// instances recovering from one outage do not retry in lockstep).
package backoff

import (
	"context"
	"errors"
	"fmt"
	"time"

	cenkalti "github.com/cenkalti/backoff/v5"
)

const (
	multiplier = 2
	jitter     = 0.2 // ±20%
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

// Retry calls fn until it succeeds or ctx ends, with no limit on attempts;
// onRetry sees every failure with the delay before the next attempt.
func Retry(
	ctx context.Context, p Policy, fn func(ctx context.Context) error, onRetry func(err error, in time.Duration),
) error {
	b := &cenkalti.ExponentialBackOff{
		InitialInterval:     p.Min,
		RandomizationFactor: jitter,
		Multiplier:          multiplier,
		MaxInterval:         p.Max,
	}

	var last error

	_, err := cenkalti.Retry(ctx, func() (struct{}, error) {
		last = fn(ctx)

		return struct{}{}, last
	}, cenkalti.WithBackOff(b), cenkalti.WithMaxElapsedTime(0), cenkalti.WithNotify(onRetry))
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (last error: %w)", ctx.Err(), last)
	}

	return err //nolint:wrapcheck // fn's error as is
}
