package backplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/filters"
	"google.golang.org/grpc"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/gate"
	"github.com/gopherex/backplane/pkg/backplane/internal/guard"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/listener"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	"github.com/gopherex/backplane/pkg/backplane/internal/telemetry"
)

const (
	healthInterval = 5 * time.Second
	uiPath         = "/_backplane/ui/"
	probesPath     = "/healthz/"
)

// ErrClosed is returned by Run after Close or a previous Run.
var ErrClosed = errors.New("backplane: service already ran or was closed")

type phase int

const (
	declaring phase = iota
	running
	closed
)

// core is the untyped engine behind Service[St]: identity, configuration,
// the node tree and the declarations.
type core struct {
	id   Identity
	cfg  config.Backplane
	log  *xlog.Logger
	conf configrt.State
	env  *env.Env
	opts options

	svc *node.Node // service root: the SDK's nodes and the author's tree
	app *node.Node // the author's tree

	health   *health.Health
	guard    guard.Guard
	gate     *gate.Gate
	internal *grpc.Server   // platform port, gRPC: internal API + health
	platform *http.ServeMux // platform port, HTTP behind the guard: UI bundle
	public   []*publicPort

	mu    sync.Mutex
	phase phase
}

// newCore builds the SDK's leading nodes; the author's tree hangs under app.
func newCore(ctx context.Context, o options, conf configrt.State, cfg config.Backplane) *core {
	id := o.id.resolve(ctx, cfg)
	log := id.logger(o.log, cfg)
	m := manifest.New(id.Service, id.Version)
	m.Config(conf.Schema(), conf.LivePaths())

	c := &core{
		id: id, cfg: cfg, log: log, conf: conf, opts: o,
		env:      env.New(id.Service, m),
		health:   health.New(log, healthInterval),
		guard:    guard.New(cfg.InternalSecret.Reveal()),
		gate:     gate.New(),
		platform: http.NewServeMux(),
	}
	c.internal = grpc.NewServer(serverOptions(append(c.guard.ServerOptions(), c.gate.ServerOptions()...)...)...)
	hv1.RegisterHealthServer(c.internal, c.health.GRPC())

	c.svc = node.New(id.Service, log, c.env)

	cn := c.svc.Child("config", node.System, false)
	cn.OnStop(func(context.Context) error { return conf.Close() })

	tel := telemetry.New(telemetry.Identity{
		Service: id.Service, Version: id.Version, Instance: id.Instance, Environment: id.Environment,
	}, log)
	tn := c.svc.Child("telemetry", node.System, false)
	tn.OnStart(tel.Start)
	tn.OnStop(tel.Stop)

	hn := c.svc.Child("health", node.System, false)
	hn.OnStart(func(ctx context.Context) error { return c.health.Start(ctx, hn) })

	c.listen("platform", listenAddr(cfg.InternalPort), c.internal, c.platformHandler())

	c.app = c.svc.Child(id.Service, node.Root, false)
	c.health.Add(health.Ready, c.app.Readiness())

	return c
}

// listen adds a listener node.
func (c *core) listen(name, addr string, g *grpc.Server, h http.Handler) {
	l := listener.New(name, addr, g, h, c.log)
	n := c.svc.Child(name, node.System, false)
	n.OnStart(func(ctx context.Context) error { return l.Start(ctx, n) })
	n.OnStop(l.Stop)
}

// Name of the service.
func (c *core) Name() string { return c.id.Service }

// Identity of the instance.
func (c *core) Identity() Identity { return c.id }

// Log is the service logger; context-aware calls carry trace and span ids.
func (c *core) Log() *xlog.Logger { return c.log }

// declare runs fn while the service is still being declared; after Run it
// is a programming error.
func (c *core) declare(what string, fn func()) {
	c.mu.Lock()
	ph := c.phase
	c.mu.Unlock()

	if ph != declaring {
		panic("backplane: " + what + " declared after Run")
	}

	fn()
}

// Close releases what Open acquired when Run will not be called. Safe to
// call more than once; Run closes by itself.
func (c *core) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase != declaring {
		return nil
	}

	c.phase = closed
	c.env.Manifest.Seal()

	if err := c.conf.Close(); err != nil {
		return fmt.Errorf("backplane: %w", err)
	}

	return nil
}

// serverOptions: telemetry without health-check noise, then extra.
func serverOptions(extra ...grpc.ServerOption) []grpc.ServerOption {
	stats := otelgrpc.NewServerHandler(otelgrpc.WithFilter(filters.Not(filters.HealthCheck())))

	return append([]grpc.ServerOption{grpc.StatsHandler(stats)}, extra...)
}

// platformHandler: probes open, everything else behind the guard.
func (c *core) platformHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(probesPath, c.health.HTTP())
	mux.Handle("/", c.guard.HTTP(c.platform))

	return mux
}

func listenAddr(port int64) string { return ":" + strconv.FormatInt(port, 10) }
