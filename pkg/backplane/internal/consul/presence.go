// Package consul keeps a service present in Consul: manifest per version,
// instance state under a TTL session (it disappears with the instance) and
// the catalog registration. Consul being absent never fails the service.
package consul

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

var (
	// ErrStateHeld: the instance state key is locked by a session that is
	// not an incarnation of this instance, or is still in its lock delay.
	ErrStateHeld = errors.New("consul: instance state held")
	// ErrDuplicateInstance: another live process runs with this instance id.
	ErrDuplicateInstance = errors.New("consul: duplicate instance id")

	errSessionLost = errors.New("consul: session expired")
)

const (
	servicesPrefix = "backplane/services/"
	checkInterval  = "10s"
	deregisterIn   = "1m"
	// lockDelay: Consul's default 15s would block a restarted instance with
	// the same id from publishing its state.
	lockDelay = time.Millisecond
	// incarnationSep separates the instance id from the incarnation in the
	// session name.
	incarnationSep = "#"
	incarnationLen = 10

	sessionTTL = 30 * time.Second
	// Renew three times per TTL; after TTL/2 of failed renews the session is
	// at risk and presence is re-established instead.
	renewsPerTTL   = 3
	renewGiveUpDiv = 2
	attemptTimeout = 20 * time.Second
	callTimeout    = 5 * time.Second
	establishFloor = 2 * time.Second
	establishCeil  = time.Minute
	renewFloor     = time.Second
	renewCeil      = 4 * time.Second
)

// timing holds the presence clocks; tests shorten them.
type timing struct {
	ttl         time.Duration
	renewEvery  time.Duration
	renewGiveUp time.Duration  // failing renews tolerated before re-establish
	attempt     time.Duration  // bound on one establish attempt
	call        time.Duration  // bound on one renew, state write or cleanup
	establish   backoff.Policy // between establish attempts
	renew       backoff.Policy // between renew attempts
}

func defaultTiming() timing {
	return timing{
		ttl:         sessionTTL,
		renewEvery:  sessionTTL / renewsPerTTL,
		renewGiveUp: sessionTTL / renewGiveUpDiv,
		attempt:     attemptTimeout,
		call:        callTimeout,
		establish:   backoff.Policy{Min: establishFloor, Max: establishCeil},
		renew:       backoff.Policy{Min: renewFloor, Max: renewCeil},
	}
}

// Identity is how the instance presents itself.
type Identity struct {
	Service      string
	Version      string
	Instance     string
	Address      string
	PlatformPort uint16
	PublicPort   uint16 // 0 when the service has no managed public routes
}

// ConfigState is the dynamic configuration seen by the presence.
type ConfigState interface {
	Effective() configrt.Effective
	OnChange(fn func())
}

// Params of New.
type Params struct {
	Client   *api.Client
	Log      *xlog.Logger
	Identity Identity
	Manifest *backplanev1.Manifest
	// Register in the catalog; false when the deployment registers.
	Register bool
	// Config may be nil.
	Config ConfigState
}

// Presence is a lifecycle component.
type Presence struct {
	client *api.Client
	log    *xlog.Logger
	id     Identity
	// name of this process's sessions: instance id and a random incarnation,
	// so a live duplicate is told apart from a crashed predecessor.
	name     string
	manifest []byte
	register bool
	config   ConfigState
	timing   timing
	changed  chan struct{}
	done     chan struct{}
	started  time.Time // set by Start before the loop runs

	mu      sync.Mutex
	session string
	running bool
	stopped bool
}

// New creates the presence; nothing is sent until Start.
func New(p Params) (*Presence, error) {
	raw, err := proto.Marshal(p.Manifest)
	if err != nil {
		return nil, fmt.Errorf("consul: marshal manifest: %w", err)
	}

	return &Presence{
		client: p.Client, log: p.Log, id: p.Identity, manifest: raw, register: p.Register, config: p.Config,
		name:    p.Identity.Instance + incarnationSep + rand.Text()[:incarnationLen],
		timing:  defaultTiming(),
		changed: make(chan struct{}, 1), done: make(chan struct{}),
	}, nil
}

// Start runs presence in the background: registration retries never block
// or fail the service.
func (p *Presence) Start(_ context.Context, g node.Group) error {
	p.started = time.Now()
	if p.config != nil {
		p.config.OnChange(p.notify)
	}

	g.Go(func(ctx context.Context) error {
		if !p.enter() {
			return nil
		}

		defer close(p.done)

		p.run(ctx)

		return nil
	})

	return nil
}

// Stop waits for the presence loop to exit (the node has already cancelled
// it), then deregisters and destroys the session so the state key
// disappears. Leaving is best effort: Consul errors are logged.
func (p *Presence) Stop(ctx context.Context) error {
	if !p.leave() {
		return nil // the loop never ran: nothing was sent
	}

	select {
	case <-p.done:
	case <-ctx.Done():
		return fmt.Errorf("consul: waiting for presence loop: %w", ctx.Err())
	}

	var errs []error

	// The catalog entry is keyed by instance id: a live duplicate holding
	// the state key owns it too, so only its holder deregisters.
	if p.register && p.holdsState(ctx) {
		errs = append(errs, p.client.Agent().ServiceDeregisterOpts(p.id.Instance, query(ctx)))
	}

	errs = append(errs, p.dropSession(ctx))

	if err := errors.Join(errs...); err != nil {
		p.log.Warn("consul leave", xlog.Err(err))
	}

	return nil
}

// holdsState reports whether our session holds the instance state key; an
// unreachable Consul counts as holding it (best-effort leave).
func (p *Presence) holdsState(ctx context.Context) bool {
	session := p.currentSession()
	if session == "" {
		return false
	}

	kv, _, err := p.client.KV().Get(p.key("instances/"+p.id.Instance), query(ctx))
	if err != nil {
		return true
	}

	return kv != nil && kv.Session == session
}

// enter marks the loop running unless Stop came first.
func (p *Presence) enter() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return false
	}

	p.running = true

	return true
}

// leave marks the presence stopped and reports whether the loop ran.
func (p *Presence) leave() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopped = true

	return p.running
}

func (p *Presence) currentSession() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.session
}

func (p *Presence) setSession(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.session = id
}

func (p *Presence) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// run establishes presence with backoff and maintains it until ctx ends.
// Only the first establish of the process may evict another incarnation of
// this instance: that one is a crashed predecessor; later it is a live
// duplicate.
func (p *Presence) run(ctx context.Context) {
	for first := true; ctx.Err() == nil; first = false {
		err := backoff.Retry(ctx, p.timing.establish, func(ctx context.Context) error {
			return p.establish(ctx, first)
		}, p.retrying)
		if err != nil {
			return // ctx ended
		}

		p.log.Info("registered in consul", xlog.String("session", p.currentSession()))
		p.maintain(ctx)
	}
}

func (p *Presence) retrying(err error, in time.Duration) {
	if errors.Is(err, ErrDuplicateInstance) {
		p.log.Error("duplicate instance id", xlog.Err(err), xlog.Duration("in", in))

		return
	}

	p.log.Warn("consul unavailable, retrying", xlog.Err(err), xlog.Duration("in", in))
}

func (p *Presence) key(suffix string) string { return servicesPrefix + p.id.Service + "/" + suffix }

func (p *Presence) establish(ctx context.Context, first bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timing.attempt)
	defer cancel()

	// The previous session of this process (lost, or kept by a failed
	// attempt) goes first: its lock would otherwise block the new one.
	if err := p.dropSession(ctx); err != nil {
		return err
	}

	manifest := &api.KVPair{Key: p.key("manifests/" + p.id.Version), Value: p.manifest}
	if _, err := p.client.KV().Put(manifest, write(ctx)); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	session, _, err := p.client.Session().Create(&api.SessionEntry{
		Name: p.name, TTL: p.timing.ttl.String(), Behavior: api.SessionBehaviorDelete, LockDelay: lockDelay,
	}, write(ctx))
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}

	p.setSession(session)

	if err := p.writeState(ctx, first); err != nil {
		return p.abandon(ctx, err)
	}

	if err := p.registerService(ctx); err != nil {
		return p.abandon(ctx, err)
	}

	return nil
}

// abandon destroys the session of a failed establish so it does not linger
// until its TTL; if that fails too, the next attempt destroys it first.
func (p *Presence) abandon(ctx context.Context, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timing.call)
	defer cancel()

	return errors.Join(cause, p.dropSession(ctx))
}

// dropSession destroys this process's current session, if any.
func (p *Presence) dropSession(ctx context.Context) error {
	session := p.currentSession()
	if session == "" {
		return nil
	}

	if _, err := p.client.Session().Destroy(session, write(ctx)); err != nil {
		return fmt.Errorf("destroy session %s: %w", session, err)
	}

	p.setSession("")

	return nil
}

func (p *Presence) registerService(ctx context.Context) error {
	if !p.register {
		return nil
	}

	reg := &api.AgentServiceRegistration{
		ID:      p.id.Instance,
		Name:    p.id.Service,
		Address: p.id.Address,
		Port:    int(p.id.PublicPort),
		Check: &api.AgentServiceCheck{
			GRPC:                           net.JoinHostPort(p.id.Address, strconv.Itoa(int(p.id.PlatformPort))),
			Interval:                       checkInterval,
			DeregisterCriticalServiceAfter: deregisterIn,
		},
	}
	if err := p.client.Agent().ServiceRegisterOpts(reg, api.ServiceRegisterOpts{}.WithContext(ctx)); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// maintain renews the session and republishes state on change; returns when
// ctx ends or the session is lost. A state write that fails stays pending
// and is retried on every renew tick until it succeeds.
func (p *Presence) maintain(ctx context.Context) {
	ticker := time.NewTicker(p.timing.renewEvery)
	defer ticker.Stop()

	dirty := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.renew(ctx); err != nil {
				if ctx.Err() == nil {
					p.log.Warn("consul session lost, re-establishing", xlog.Err(err))
				}

				return
			}
		case <-p.changed:
			dirty = true
		}

		if dirty {
			dirty = !p.publish(ctx)
		}
	}
}

// renew renews the session, retrying failures for up to renewGiveUp — well
// inside the TTL — before declaring it lost. A session Consul no longer
// knows is lost at once.
func (p *Presence) renew(ctx context.Context) error {
	session := p.currentSession()

	ctx, cancel := context.WithTimeout(ctx, p.timing.renewGiveUp)
	defer cancel()

	lost := false

	err := backoff.Retry(ctx, p.timing.renew, func(ctx context.Context) error {
		ctx, stop := context.WithTimeout(ctx, p.timing.call)
		defer stop()

		entry, _, err := p.client.Session().Renew(session, write(ctx))
		if err != nil {
			return fmt.Errorf("renew session %s: %w", session, err)
		}

		lost = entry == nil

		return nil
	}, func(err error, in time.Duration) {
		p.log.Warn("consul session renew failed, retrying", xlog.Err(err), xlog.Duration("in", in))
	})

	switch {
	case err != nil:
		return fmt.Errorf("renew: %w", err)
	case lost:
		return fmt.Errorf("%w: %s", errSessionLost, session)
	}

	return nil
}

// publish writes the instance state; false when it has to be retried.
func (p *Presence) publish(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timing.call)
	defer cancel()

	if err := p.writeState(ctx, false); err != nil {
		if ctx.Err() == nil {
			p.log.Warn("instance state, retrying on next renew", xlog.Err(err))
		}

		return false
	}

	return true
}

// revision is the console's config revision next to the values
// (config/<service>/_revision), read right after a change was applied; 0
// when absent or unreadable.
func (p *Presence) revision(ctx context.Context) uint64 {
	kv, _, err := p.client.KV().Get("config/"+p.id.Service+"/_revision", query(ctx))
	if err != nil || kv == nil {
		return 0
	}

	n, err := strconv.ParseUint(strings.TrimSpace(string(kv.Value)), 10, 64)
	if err != nil {
		return 0
	}

	return n
}

func (p *Presence) state(ctx context.Context) ([]byte, error) {
	state := &backplanev1.InstanceState{
		Id:           p.id.Instance,
		Service:      p.id.Service,
		Version:      p.id.Version,
		Address:      p.id.Address,
		PlatformPort: uint32(p.id.PlatformPort),
		StartedAt:    timestamppb.New(p.started),
	}

	if p.config != nil {
		state.ConfigRevision = p.revision(ctx)

		eff := p.config.Effective()
		state.Config, state.Sources = eff.Values, eff.Sources

		if eff.Err != nil {
			state.ConfigError = eff.Err.Error()
		}
	}

	raw, err := proto.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("instance state: marshal: %w", err)
	}

	return raw, nil
}

// writeState publishes the instance state under the current session. first
// allows evicting another incarnation of this instance (see run).
func (p *Presence) writeState(ctx context.Context, first bool) error {
	raw, err := p.state(ctx)
	if err != nil {
		return err
	}

	key := p.key("instances/" + p.id.Instance)

	ok, err := p.acquire(ctx, key, raw)
	if err == nil && !ok {
		if err = p.evict(ctx, key, first); err == nil {
			ok, err = p.acquire(ctx, key, raw)
		}
	}

	switch {
	case err != nil:
		return fmt.Errorf("instance state: %w", err)
	case !ok:
		return fmt.Errorf("%w: %s", ErrStateHeld, key)
	}

	return nil
}

func (p *Presence) acquire(ctx context.Context, key string, value []byte) (bool, error) {
	ok, _, err := p.client.KV().Acquire(&api.KVPair{Key: key, Value: value, Session: p.currentSession()}, write(ctx))
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", key, err)
	}

	return ok, nil
}

// evict destroys the session holding key when it is a stale one of this
// process, or — on the first establish only — of an earlier incarnation of
// this instance. Another live incarnation is ErrDuplicateInstance; any other
// holder is left alone (the acquire retry reports ErrStateHeld).
func (p *Presence) evict(ctx context.Context, key string, first bool) error {
	holder, err := p.holder(ctx, key)
	if err != nil || holder == nil || !p.sameInstance(holder.Name) {
		return err
	}

	if holder.Name != p.name && !first {
		return fmt.Errorf("%w: %s held by session %s (%s), this is %s",
			ErrDuplicateInstance, key, holder.ID, holder.Name, p.name)
	}

	p.log.Info("evicting stale session of this instance", xlog.String("session", holder.ID),
		xlog.String("name", holder.Name))

	if _, err := p.client.Session().Destroy(holder.ID, write(ctx)); err != nil {
		return fmt.Errorf("destroy session %s: %w", holder.ID, err)
	}

	return nil
}

// holder is the session locking key, nil when none does.
func (p *Presence) holder(ctx context.Context, key string) (*api.SessionEntry, error) {
	kv, _, err := p.client.KV().Get(key, query(ctx))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}

	if kv == nil || kv.Session == "" {
		return nil, nil //nolint:nilnil // an unlocked key has no holder
	}

	holder, _, err := p.client.Session().Info(kv.Session, query(ctx))
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", kv.Session, err)
	}

	return holder, nil
}

// sameInstance reports whether a session name is an incarnation of this
// instance id.
func (p *Presence) sameInstance(name string) bool {
	inc, ok := strings.CutPrefix(name, p.id.Instance+incarnationSep)

	return ok && inc != "" && !strings.Contains(inc, incarnationSep)
}

func write(ctx context.Context) *api.WriteOptions { return (&api.WriteOptions{}).WithContext(ctx) }
func query(ctx context.Context) *api.QueryOptions { return (&api.QueryOptions{}).WithContext(ctx) }
