// Package consul keeps a service present in Consul: manifest per version,
// instance state under a TTL session (it disappears with the instance) and
// the catalog registration. Consul being absent never fails the service.
package consul

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
)

// ErrStateHeld: the instance state key is locked by another instance.
var ErrStateHeld = errors.New("consul: instance state held by another instance")

const (
	servicesPrefix = "backplane/services/"
	sessionTTL     = "30s"
	renewEvery     = 10 * time.Second
	retryBase      = 2 * time.Second
	retryMax       = time.Minute
	retryFactor    = 2
	checkInterval  = "10s"
	deregisterIn   = "1m"
	// lockDelay: Consul's default 15s would block a restarted instance with
	// the same id from publishing its state.
	lockDelay = time.Millisecond
)

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
	Effective() config.Effective
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
	client   *api.Client
	log      *xlog.Logger
	id       Identity
	manifest []byte
	register bool
	config   ConfigState
	started  time.Time
	changed  chan struct{}
	done     chan struct{}

	mu      sync.Mutex
	session string
}

// New creates the presence; nothing is sent until Start.
func New(p Params) (*Presence, error) {
	raw, err := proto.Marshal(p.Manifest)
	if err != nil {
		return nil, fmt.Errorf("consul: marshal manifest: %w", err)
	}

	return &Presence{
		client: p.Client, log: p.Log, id: p.Identity, manifest: raw, register: p.Register, config: p.Config,
		changed: make(chan struct{}, 1), done: make(chan struct{}),
	}, nil
}

// Name implements lifecycle.Component.
func (p *Presence) Name() string { return "consul" }

// Start runs presence in the background: registration retries never block
// or fail the service.
func (p *Presence) Start(_ context.Context, g lifecycle.Group) error {
	p.started = time.Now()
	if p.config != nil {
		p.config.OnChange(p.notify)
	}

	g.Go("consul", func(ctx context.Context) error {
		defer close(p.done)

		p.run(ctx)

		return nil
	})

	return nil
}

// Stop waits for the presence loop to exit (the lifecycle has already
// cancelled it), then deregisters and destroys the session so the state key
// disappears. Leaving is best effort: Consul errors are logged.
func (p *Presence) Stop(ctx context.Context) error {
	select {
	case <-p.done:
	case <-ctx.Done():
		return fmt.Errorf("consul: waiting for presence loop: %w", ctx.Err())
	}

	var errs []error
	if p.register {
		errs = append(errs, p.client.Agent().ServiceDeregisterOpts(p.id.Instance, query(ctx)))
	}

	if session := p.currentSession(); session != "" {
		_, err := p.client.Session().Destroy(session, write(ctx))
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		p.log.Warn("consul leave", xlog.Err(err))
	}

	return nil
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
func (p *Presence) run(ctx context.Context) {
	delay := retryBase

	for ctx.Err() == nil {
		if err := p.establish(ctx); err != nil {
			p.log.Warn("consul unavailable, retrying", xlog.Err(err), xlog.Duration("in", delay))

			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}

			delay = min(delay*retryFactor, retryMax)

			continue
		}

		delay = retryBase

		p.log.Info("registered in consul")
		p.maintain(ctx)
	}
}

func (p *Presence) key(suffix string) string { return servicesPrefix + p.id.Service + "/" + suffix }

func (p *Presence) establish(ctx context.Context) error {
	manifest := &api.KVPair{Key: p.key("manifests/" + p.id.Version), Value: p.manifest}
	if _, err := p.client.KV().Put(manifest, write(ctx)); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	session, _, err := p.client.Session().Create(&api.SessionEntry{
		Name: p.id.Instance, TTL: sessionTTL, Behavior: api.SessionBehaviorDelete, LockDelay: lockDelay,
	}, write(ctx))
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}

	p.setSession(session)

	if err := p.writeState(ctx); err != nil {
		return err
	}

	if !p.register {
		return nil
	}

	reg := &api.AgentServiceRegistration{
		ID:      p.id.Instance,
		Name:    p.id.Service,
		Address: p.id.Address,
		Port:    int(p.id.PublicPort),
		Check: &api.AgentServiceCheck{
			GRPC:                           p.id.Address + ":" + strconv.Itoa(int(p.id.PlatformPort)),
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
// ctx ends or the session is lost.
func (p *Presence) maintain(ctx context.Context) {
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry, _, err := p.client.Session().Renew(p.currentSession(), write(ctx))
			if err != nil || entry == nil {
				p.log.Warn("consul session lost", xlog.Err(err))

				return
			}
		case <-p.changed:
			if err := p.writeState(ctx); err != nil {
				p.log.Warn("instance state", xlog.Err(err))
			}
		}
	}
}

func (p *Presence) writeState(ctx context.Context) error {
	state := &backplanev1.InstanceState{
		Id:           p.id.Instance,
		Service:      p.id.Service,
		Version:      p.id.Version,
		Address:      p.id.Address,
		PlatformPort: uint32(p.id.PlatformPort),
		StartedAt:    timestamppb.New(p.started),
	}
	if p.config != nil {
		eff := p.config.Effective()
		state.Config, state.Sources = eff.Values, eff.Sources

		if eff.Err != nil {
			state.ConfigError = eff.Err.Error()
		}
	}

	raw, err := proto.Marshal(state)
	if err != nil {
		return fmt.Errorf("instance state: marshal: %w", err)
	}

	key := p.key("instances/" + p.id.Instance)

	ok, err := p.acquire(ctx, key, raw)
	if err == nil && !ok {
		// Held by an earlier incarnation of this instance (crash, quick
		// restart): the same instance id is the same instance, newest wins.
		if err = p.evictStale(ctx, key); err == nil {
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

// evictStale destroys the session holding key when it belongs to this
// instance id.
func (p *Presence) evictStale(ctx context.Context, key string) error {
	kv, _, err := p.client.KV().Get(key, query(ctx))
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}

	if kv == nil || kv.Session == "" {
		return nil
	}

	holder, _, err := p.client.Session().Info(kv.Session, query(ctx))
	if err != nil {
		return fmt.Errorf("session %s: %w", kv.Session, err)
	}

	if holder == nil || holder.Name != p.id.Instance {
		return nil
	}

	p.log.Info("evicting stale session of this instance", xlog.String("session", holder.ID))

	if _, err := p.client.Session().Destroy(holder.ID, write(ctx)); err != nil {
		return fmt.Errorf("destroy session %s: %w", holder.ID, err)
	}

	return nil
}

func write(ctx context.Context) *api.WriteOptions { return (&api.WriteOptions{}).WithContext(ctx) }
func query(ctx context.Context) *api.QueryOptions { return (&api.QueryOptions{}).WithContext(ctx) }
