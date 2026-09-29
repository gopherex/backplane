package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/backplane/internal/config"
	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/xds"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// State is the server's tree under the Root, in start order: the store,
// the registry, then the components built on them. Components stop in
// reverse, before what they were given.
type State struct {
	// Consul is backplane's client (the SDK's own is not shared): the
	// registry reads through it, configuration writes KV through it.
	Consul *api.Client
	// Store is PostgreSQL: required — the start waits for it (migrated),
	// readiness follows its ping.
	Store deps.Dependency[*store.Store]
	// Registry is the installation as Consul sees it; everything else reads
	// its snapshots (registry.Source).
	Registry *registry.Registry
	// Config keeps Live-value overrides: revisions in PostgreSQL, delivery
	// and reconciliation to Consul KV config/, the console's ConfigService.
	Config *config.Manager
	// XDS is the control plane of Envoy: ADS on its own listener, one
	// snapshot rebuilt from the registry.
	XDS *xds.Server
	// Console is the console's HTTP on its own listener: auth, /ws
	// (backplane's own API and the relay to the services' internal API),
	// plugin bundles.
	Console *console.Console

	// Extension points — one field and one constructor line in NewState per
	// component, in the order they depend on each other:
	//
	//	XDS     *xds.Server         // internal/xds: ADS for Envoy
	//	Console *console.Server     // internal/console: HTTP, ws-proto, relay
}

// NewState builds the server's tree. Every component gets what it needs by
// argument: cfg sections, st.Consul, st.Store, st.Registry (as a
// registry.Source).
func NewState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()

	client, err := consulClient(cfg.Consul)
	if err != nil {
		return nil, err
	}

	st := &State{Consul: client}
	st.Store = deps.NewDependency(root, store.New(cfg.PG.DSN))
	st.Registry = registry.New(root, client)
	st.Config = config.New(root, cfg.LiveConfig, client, st.Store, st.Registry, config.Author(sessionAuthor))
	st.XDS = xds.New(root, xdsConfig(cfg, root.Identity().Advertise), st.Registry)
	st.Console = console.New(root, cfg.consoleSettings(), console.NewPG(st.Store), st.Registry,
		console.WithServices(st.Config.Register))

	return st, nil
}

// Ready is the server's readiness beyond its required dependencies (which
// are readiness already): every component that must be up before traffic.
// A component with a readiness of its own adds its line here.
func (st *State) Ready(context.Context) error {
	var errs []error

	if err := st.Registry.Ready(); err != nil {
		errs = append(errs, fmt.Errorf("registry: %w", err))
	}

	if err := st.XDS.Ready(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// sessionAuthor names the author of a configuration revision: the console
// session that made the call, "admin" when there is none.
func sessionAuthor(ctx context.Context) string {
	if id, ok := console.SessionID(ctx); ok {
		return "console:" + id.String()
	}

	return "admin"
}
