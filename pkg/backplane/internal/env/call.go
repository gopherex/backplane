package env

import (
	"context"
	"time"
)

// DefaultHookTimeout bounds a hook call nothing else bounds: no deadline on
// the context, none on the call or the declaration, no configured one.
const DefaultHookTimeout = 30 * time.Second

type callKeyCtx struct{}

// WithCallKey marks the hook call made with ctx as idempotent under key:
// the transport runs one call per key. Empty key: an ordinary call.
func WithCallKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}

	return context.WithValue(ctx, callKeyCtx{}, key)
}

// CallKeyOf is the idempotency key WithCallKey set, or "".
func CallKeyOf(ctx context.Context) string {
	k, _ := ctx.Value(callKeyCtx{}).(string)

	return k
}

// SetHookTimeout sets the platform default deadline of a hook call; 0 or
// less restores DefaultHookTimeout.
func (e *Env) SetHookTimeout(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.hookTimeout = d
}

// HookTimeout is the platform default deadline of a hook call: the
// configured one or DefaultHookTimeout. Never zero.
func (e *Env) HookTimeout() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.hookTimeout <= 0 {
		return DefaultHookTimeout
	}

	return e.hookTimeout
}

// ActivityInfo is what the transport knows about the activity execution a
// handler serves.
type ActivityInfo struct {
	Attempt   int32     // 1 for the first
	Binding   string    // binding or rule that called, from the envelope
	Step      string    // its step
	Key       string    // "<workflow id>/<activity id>": the same for every attempt
	Deadline  time.Time // of this attempt; zero when unbounded
	Heartbeat func(details ...any)
}

type activityCtx struct{}

// WithActivityInfo attaches info to a handler's ctx.
func WithActivityInfo(ctx context.Context, info ActivityInfo) context.Context {
	return context.WithValue(ctx, activityCtx{}, info)
}

// ActivityInfoOf is the info attached by WithActivityInfo.
func ActivityInfoOf(ctx context.Context) (ActivityInfo, bool) {
	info, ok := ctx.Value(activityCtx{}).(ActivityInfo)

	return info, ok
}
