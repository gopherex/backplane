package backplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/consul"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
	"github.com/gopherex/backplane/pkg/backplane/internal/listener"
	"github.com/gopherex/backplane/pkg/backplane/internal/telemetry"
	"github.com/gopherex/backplane/pkg/backplane/internal/tree"
)

// ErrRunning is returned by a second Run.
var ErrRunning = errors.New("backplane: service already running")

// Run assembles the components and blocks until ctx ends, a signal arrives
// or a goroutine fails.
func (c *core) Run(ctx context.Context) error {
	if !c.start() {
		return ErrRunning
	}
	defer c.detach()

	c.manifest.Nodes(nodes(c.tree.Nodes()))

	for _, p := range c.tree.Probes() {
		c.health.Add(health.Ready, p)
	}

	m, err := c.manifest.Build()
	if err != nil {
		return errors.Join(fmt.Errorf("backplane: manifest: %w", err), c.conf.Close())
	}

	for _, comp := range c.components(m) {
		c.lc.Add(comp)
	}

	if err := c.lc.Run(ctx); err != nil {
		return fmt.Errorf("backplane: %w", err)
	}

	return nil
}

// components in start order; they stop in reverse.
func (c *core) components(m *backplanev1.Manifest) []lifecycle.Component {
	id := telemetry.Identity{
		Service: c.id.Service, Version: c.id.Version, Instance: c.id.Instance, Environment: c.id.Environment,
	}
	comps := []lifecycle.Component{
		configCloser{c.conf},
		telemetry.New(id, c.log),
		c.health,
		listener.New("platform", listenAddr(c.cfg.InternalPort), c.internal, c.platformHandler(), c.log),
		c.tree,
	}

	for _, p := range c.public {
		comps = append(comps, listener.New("public"+p.addr, p.addr, p.grpc, p.handler(), c.log))
	}

	if presence := c.presence(m); presence != nil {
		comps = append(comps, presence)
	}

	return append(comps, servingGate{c.health})
}

// presence is the Consul component, or nil when Consul is not configured or
// unusable — a warning, not an error, for a service.
func (c *core) presence(m *backplanev1.Manifest) *consul.Presence {
	if err := c.conf.Degraded(); err != nil {
		c.log.Warn("config without consul layer, retrying", xlog.Err(err))
	}

	if !c.cfg.Consul.Enabled() {
		c.log.Warn("consul not configured: unregistered, config from env/file only")

		return nil
	}

	client, err := c.cfg.Consul.Client()
	if err != nil {
		c.log.Warn("consul client", xlog.Err(err))

		return nil
	}

	presence, err := consul.New(consul.Params{
		Client: client,
		Log:    c.log,
		Identity: consul.Identity{
			Service:      c.id.Service,
			Version:      c.id.Version,
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

func nodes(infos []tree.Info) []*backplanev1.Node {
	kinds := map[tree.Kind]backplanev1.NodeKind{
		tree.Component:  backplanev1.NodeKind_NODE_KIND_COMPONENT,
		tree.Dependency: backplanev1.NodeKind_NODE_KIND_DEPENDENCY,
		tree.Singleton:  backplanev1.NodeKind_NODE_KIND_SINGLETON,
	}

	out := make([]*backplanev1.Node, len(infos))
	for i, n := range infos {
		out[i] = &backplanev1.Node{Path: n.Path, Kind: kinds[n.Kind], Optional: n.Optional}
	}

	return out
}

// port16 narrows a schema-validated port (1..65535).
func port16(p int64) uint16 { return uint16(p) } //nolint:gosec // validated by the config schema

// servingGate opens readiness last and closes it first.
type servingGate struct{ h *health.Health }

func (servingGate) Name() string { return "serving" }

func (g servingGate) Start(ctx context.Context, _ lifecycle.Group) error {
	g.h.Serving(ctx, true)

	return nil
}

func (g servingGate) Stop(ctx context.Context) error {
	g.h.Serving(ctx, false)

	return nil
}

// configCloser closes the configuration after everything that reads it has
// stopped.
type configCloser struct{ conf configState }

func (configCloser) Name() string { return "config" }

func (configCloser) Start(context.Context, lifecycle.Group) error { return nil }

func (c configCloser) Stop(context.Context) error {
	if err := c.conf.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}

	return nil
}
