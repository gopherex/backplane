package backplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/consul"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

const devVersion = "0.0.0"

// Run brings the service up and blocks until ctx ends, SIGINT/SIGTERM
// arrives or a node's goroutine fails; then stops everything within the
// shutdown budget. A stop requested during start is not an error.
func (c *core) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.phase != declaring {
		c.mu.Unlock()

		return ErrClosed
	}

	c.phase = running
	c.mu.Unlock()

	if c.opts.slog {
		prev := slog.Default()

		slog.SetDefault(slog.New(xlog.NewSlogHandler(c.log)))

		defer slog.SetDefault(prev)
	}

	m, err := c.seal()
	if err != nil {
		return errors.Join(fmt.Errorf("backplane: manifest: %w", err), c.conf.Close())
	}

	c.tail(m)

	if c.cfg.InternalSecret.Reveal() == "" {
		c.log.Warn("internal secret not set: the internal API and UI on the platform port are open to anyone who reaches it")
	}

	runCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	var cause error

	if err := c.svc.Start(runCtx); err != nil {
		if runCtx.Err() == nil {
			cause = fmt.Errorf("backplane: start: %w", err)
		} else {
			c.log.Info("stopped during start")
		}
	} else {
		select {
		case <-runCtx.Done():
			c.log.Info("stopping")
		case err := <-c.svc.Failed():
			cause = fmt.Errorf("backplane: %w", err)
		}
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.Shutdown.Timeout)
	defer cancel()

	if err := c.svc.Stop(stopCtx); err != nil {
		return errors.Join(cause, fmt.Errorf("backplane: stop: %w", err))
	}

	return cause
}

// seal ends declarations and builds the manifest; an unstamped version gets
// the manifest's content hash so dev builds do not overwrite each other.
func (c *core) seal() (*backplanev1.Manifest, error) {
	c.env.Manifest.Nodes(nodes(c.app))
	c.env.Manifest.Seal()

	m, err := c.env.Manifest.Build()
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	if m.GetVersion() == devVersion {
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		sum := sha256.Sum256(raw)
		m.Version = devVersion + "+" + hex.EncodeToString(sum[:6])
	}

	return m, nil
}

// tail adds the nodes that follow the author's tree: internal API gate,
// reactors, the Temporal worker, public ports, the drain pause, Consul
// presence and the serving gate. They start in this order and stop in
// reverse: traffic of every kind ends before the tree stops.
func (c *core) tail(m *backplanev1.Manifest) {
	gn := c.svc.Child("internal-api", node.System, false)
	gn.OnStart(func(context.Context) error { c.gate.Open(); return nil })
	gn.OnStop(c.gate.Close)

	c.work(m)

	for _, p := range c.public {
		c.listen("public"+p.addr, p.addr, p.grpc, p.handler())
	}

	dn := c.svc.Child("drain", node.System, false)
	dn.OnStop(func(ctx context.Context) error { return pause(ctx, c.cfg.Shutdown.Drain) })

	if presence := c.presence(m); presence != nil {
		pn := c.svc.Child("consul", node.System, false)
		pn.OnStart(func(ctx context.Context) error { return presence.Start(ctx, pn) })
		pn.OnStop(presence.Stop)
	}

	sn := c.svc.Child("serving", node.System, false)
	sn.OnStart(func(ctx context.Context) error { c.health.Serving(ctx, true); return nil })
	sn.OnStop(func(ctx context.Context) error { c.health.Serving(ctx, false); return nil })
}

// work adds the event reactors and the Temporal worker when the service
// declared something for them, and the reconciliation of its Temporal
// schedules; without the transport it warns.
func (c *core) work(m *backplanev1.Manifest) {
	switch {
	case len(m.GetSubscriptions()) == 0:
	case c.broker == nil:
		c.log.Warn("reactors declared but NATS is not configured: events will not be consumed")
	default:
		n := c.svc.Child("reactors", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.broker.StartReactors(ctx, n) })
		n.OnStop(c.broker.StopReactors)
	}

	switch {
	case len(m.GetActivities()) == 0 && len(m.GetHooks()) == 0 && len(c.env.WorkerRegistrations()) == 0:
	case c.temporal == nil:
		c.log.Warn("activities, hooks or workflows declared but Temporal is not configured: they are unavailable")
	case !c.cfg.Temporal.Worker.Enabled:
		c.log.Info("temporal worker disabled on this replica: other replicas serve the task queue")
	default:
		n := c.svc.Child("worker", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.temporal.StartWorker(ctx, n) })
		n.OnStop(c.temporal.StopWorker)
	}

	switch {
	case c.temporal != nil:
		// Also with nothing declared: schedules left by an earlier version
		// are deleted.
		n := c.svc.Child("schedules", node.System, false)
		n.OnStart(func(ctx context.Context) error { return c.temporal.ReconcileSchedules(ctx, n) })
	case len(m.GetSchedules()) > 0:
		c.log.Warn("schedules declared but Temporal is not configured: they are not created")
	}
}

// pause waits d or until ctx ends: load balancers catch up with the
// deregistration before listeners close.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain: %w", ctx.Err())
	}
}

// presence is the Consul component, or nil when Consul is not configured —
// a warning, not an error, for a service.
func (c *core) presence(m *backplanev1.Manifest) *consul.Presence {
	if err := c.conf.Degraded(); err != nil {
		c.log.Warn("config without consul layer, retrying", xlog.Err(err))
	}

	client := c.conf.Consul()
	if client == nil {
		c.log.Warn("consul not configured: unregistered, config from env/file only")

		return nil
	}

	presence, err := consul.New(consul.Params{
		Client: client,
		Log:    c.log,
		Identity: consul.Identity{
			Service:      c.id.Service,
			Version:      m.GetVersion(),
			Instance:     c.id.Instance,
			Address:      c.id.Advertise,
			PlatformPort: port16(c.cfg.InternalPort),
			PublicPort:   c.primaryPort(),
		},
		Manifest: m,
		Register: c.cfg.Consul.Register,
		Config:   c.conf,
	})
	if err != nil {
		c.log.Warn("consul presence", xlog.Err(err))

		return nil
	}

	return presence
}

// nodes describes the author's tree for the manifest.
func nodes(app *node.Node) []*backplanev1.Node {
	kinds := map[node.Kind]backplanev1.NodeKind{
		node.Component:  backplanev1.NodeKind_NODE_KIND_COMPONENT,
		node.Dependency: backplanev1.NodeKind_NODE_KIND_DEPENDENCY,
		node.Singleton:  backplanev1.NodeKind_NODE_KIND_SINGLETON,
	}

	var out []*backplanev1.Node

	app.Walk(func(n *node.Node) {
		if k, ok := kinds[n.Kind()]; ok {
			out = append(out, &backplanev1.Node{Path: n.Path(), Kind: k, Optional: n.Optional()})
		}
	})

	return out
}

// port16 narrows a schema-validated port (1..65535).
func port16(p int64) uint16 { return uint16(p) } //nolint:gosec // validated by the config schema
