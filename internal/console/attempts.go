package console

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	valkeygo "github.com/valkey-io/valkey-go"

	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/infra/valkey"
)

// ErrAttempts: the login attempts cannot be counted; the login is refused.
var ErrAttempts = errors.New("console: login attempts unavailable")

// Attempts counts login attempts for brute force protection (§11.3), shared
// by every replica: Valkey in the server, memory in tests. When they cannot
// be counted the login is refused.
type Attempts interface {
	// Allow takes one attempt from addr; refused, it is how long to wait.
	Allow(ctx context.Context, addr string) (time.Duration, error)
	// Failed records a wrong token from anywhere.
	Failed(ctx context.Context) error
	// Succeeded resets the global backoff.
	Succeeded(ctx context.Context) error
}

// LoginLimits of Valkey attempts.
type LoginLimits struct {
	// Every address has Burst attempts per Window.
	Burst  int
	Window time.Duration
	// After Threshold failures (any address, none older than Forget apart)
	// every login waits Base, doubling per further failure up to Max; a
	// success resets it.
	Threshold int
	Base, Max time.Duration
	Forget    time.Duration
}

const (
	loginBurst     = 5
	loginThreshold = 10
)

// DefaultLoginLimits: 5 attempts a minute per address; after 10 failures
// in a row 1s, doubling up to a minute.
func DefaultLoginLimits() LoginLimits {
	return LoginLimits{
		Burst: loginBurst, Window: time.Minute,
		Threshold: loginThreshold, Base: time.Second, Max: time.Minute, Forget: time.Hour,
	}
}

// Keys share one hash slot ({login}), so a script touches them on one
// cluster node.
const (
	keyBlocked  = "backplane:{login}:blocked"
	keyFailures = "backplane:{login}:failures"
	keyAddr     = "backplane:{login}:addr:"
)

// allowScript: the remaining global backoff, else one attempt of the
// address's window — 0 allowed, otherwise milliseconds to wait.
const allowScript = `
local blocked = redis.call('PTTL', KEYS[1])
if blocked > 0 then return blocked end
local n = redis.call('INCR', KEYS[2])
if n == 1 or redis.call('PTTL', KEYS[2]) < 0 then redis.call('PEXPIRE', KEYS[2], ARGV[2]) end
if n <= tonumber(ARGV[1]) then return 0 end
local ttl = redis.call('PTTL', KEYS[2])
if ttl < 1 then return 1 end
return ttl
`

// failedScript: one more failure; from the threshold on, the backoff.
const failedScript = `
local n = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
local over = n - tonumber(ARGV[1])
if over < 0 then return n end
local wait = tonumber(ARGV[2]) * 2 ^ math.min(over, 16)
if wait > tonumber(ARGV[3]) then wait = tonumber(ARGV[3]) end
redis.call('SET', KEYS[2], n, 'PX', math.floor(wait))
return n
`

// Valkey counts attempts in Valkey.
type Valkey struct {
	client        deps.Dependency[valkey.Client]
	limits        LoginLimits
	allow, failed *valkeygo.Lua
}

var _ Attempts = Valkey{}

// NewValkey counts attempts in Valkey with limits; the client is read once
// it is provided (from the console's start on).
func NewValkey(client deps.Dependency[valkey.Client], limits LoginLimits) Valkey {
	return Valkey{
		client: client, limits: limits,
		allow: valkeygo.NewLuaScript(allowScript), failed: valkeygo.NewLuaScript(failedScript),
	}
}

// Allow implements Attempts.
func (v Valkey) Allow(ctx context.Context, addr string) (time.Duration, error) {
	wait, err := v.allow.Exec(ctx, v.client.Get(), []string{keyBlocked, keyAddr + addr},
		[]string{strconv.Itoa(v.limits.Burst), millis(v.limits.Window)}).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return time.Duration(wait) * time.Millisecond, nil
}

// Failed implements Attempts.
func (v Valkey) Failed(ctx context.Context) error {
	err := v.failed.Exec(ctx, v.client.Get(), []string{keyFailures, keyBlocked}, []string{
		strconv.Itoa(v.limits.Threshold), millis(v.limits.Base), millis(v.limits.Max), millis(v.limits.Forget),
	}).Error()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return nil
}

// Succeeded implements Attempts.
func (v Valkey) Succeeded(ctx context.Context) error {
	c := v.client.Get()
	if err := c.Do(ctx, c.B().Del().Key(keyFailures).Build()).Error(); err != nil {
		return fmt.Errorf("%w: %w", ErrAttempts, err)
	}

	return nil
}

func millis(d time.Duration) string { return strconv.FormatInt(max(d.Milliseconds(), 1), 10) }
