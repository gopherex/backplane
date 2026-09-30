package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/backplane/internal/audit"
	"github.com/gopherex/backplane/internal/bindings"
	"github.com/gopherex/backplane/internal/config"
	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/internal/executor"
	"github.com/gopherex/backplane/internal/obs"
	"github.com/gopherex/backplane/internal/ops"
	"github.com/gopherex/backplane/internal/otlp"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/rules"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/xds"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/workflows"
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
	// Bindings keeps bindings (hook -> steps) and rules (event -> steps):
	// versions in PostgreSQL validated against the latest manifests, the
	// console's BindingService and RuleService.
	Bindings *bindings.Manager
	// XDS is the control plane of Envoy: ADS on its own listener, one
	// snapshot rebuilt from the registry.
	XDS *xds.Server
	// Rules consumes events for the rules and starts their runs.
	Rules *rules.Engine
	// Ops is the console's operations on NATS and Temporal through the
	// SDK's own clients: events, streams, consumers, dead letters;
	// workflows, runs, schedules; hook and activity calls (their workflows
	// run on backplane's task queue).
	Ops *ops.Ops
	// Executor answers every hook over Temporal Nexus (an endpoint per
	// service with hooks, a Nexus worker on backplane's queue) and runs
	// bindings and rules as the workflow backplane.Binding.v1 there; the
	// console's run RPCs of BindingService.
	Executor *executor.Executor
	// Console is the console's HTTP on its own listener: auth, /ws
	// (backplane's own API and the relay to the services' internal API),
	// plugin bundles.
	Console *console.Console
	// Obs reads deployment-owned stores; no dependency on their readiness.
	Obs         *obs.Service
	internalObs bool
	// Audit delivers durable control events and serves AuditService.
	Audit *audit.Service

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

	var consoleOptions []console.Option

	if cfg.Console.AssetsDir != "" {
		shell := os.DirFS(cfg.Console.AssetsDir)
		if _, err := fs.Stat(shell, "index.html"); err != nil {
			return nil, fmt.Errorf("console assets: %w", err)
		}

		consoleOptions = append(consoleOptions, console.WithShell(shell))
	}

	if cfg.OTLP.URL != "" {
		admission, err := otlp.New(cfg.OTLP)
		if err != nil {
			return nil, fmt.Errorf("OTLP admission: %w", err)
		}

		consoleOptions = append(consoleOptions, console.WithTelemetry(admission))

		root.OnStop(func(context.Context) error { admission.Close(); return nil })
	}

	client, err := consulClient(cfg.Consul)
	if err != nil {
		return nil, err
	}

	st := &State{Consul: client, internalObs: cfg.InternalSecret.Reveal() != ""}

	st.Store = deps.NewDependency(root, store.New(cfg.PG.DSN))
	st.Registry = registry.New(root, client)
	st.Ops = ops.New(root, st.Registry, ops.Author(sessionAuthor), ops.Namespace(cfg.Temporal.Namespace))
	st.Audit = audit.New(root, st.Store, audit.WithRetention(cfg.Audit.Retention), audit.WithRunQueue(st.Ops.RunQueue))

	st.Obs, err = obs.New(cfg.Obs, obs.WithRegistry(st.Registry))
	if err != nil {
		return nil, fmt.Errorf("observability: %w", err)
	}

	root.OnStop(func(context.Context) error { st.Obs.Close(); return nil })
	st.Config = config.New(root, cfg.LiveConfig, client, st.Store, st.Registry, config.Author(sessionAuthor))
	st.Bindings = bindings.New(root, st.Store, st.Registry, bindings.Author(sessionAuthor))
	st.XDS = xds.New(root, xdsConfig(cfg, root.Identity().Advertise), st.Registry)
	workflows.Register(root, ops.RegisterWorkflows)
	st.Executor = executor.New(root, st.Bindings, st.Registry, executor.Author(sessionAuthor),
		executor.Namespace(cfg.Temporal.Namespace), executor.Runs(st.Ops.Workflows()),
		executor.AbsenceGrace(cfg.Nexus.AbsenceGrace), executor.Resync(cfg.Nexus.ReconcileInterval))
	workflows.Register(root, st.Executor.RegisterWorkflows)
	st.Rules = rules.New(root, st.Bindings, st.Registry, rules.WithRuns(st.Ops.Workflows()))
	consoleOptions = append(consoleOptions,
		console.WithServices(st.Config.Register), console.WithServices(st.Audit.Commands(st.Ops.Register, sessionAuthor)),
		console.WithServices(st.Audit.Commands(st.Executor.Register(st.Bindings.BindingAPI()), sessionAuthor)),
		console.WithServices(st.Audit.Commands(st.Rules.Register, sessionAuthor)), console.WithServices(st.Bindings.Register),
		console.WithServices(st.Obs.Register),
		console.WithServices(st.Audit.Register))
	st.Console = console.New(root, cfg.consoleSettings(), console.NewPG(st.Store), st.Registry, consoleOptions...)

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
