package backplane

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/guard"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/tree"
)

const (
	healthInterval = 5 * time.Second
	uiPath         = "/_backplane/ui/"
	probesPath     = "/healthz/"
)

// configState is what the core needs from *config.Runtime[C].
type configState interface {
	Schema() *sp.Schema
	Live() []xconf.Path
	Effective() config.Effective
	OnChange(fn func())
	Degraded() error
	Close() error
}

// core is the untyped engine behind Service[St]: identity, configuration,
// the node tree, declarations and the lifecycle that runs them.
type core struct {
	id   Identity
	cfg  config.Backplane
	log  *xlog.Logger
	conf configState
	lc   *lifecycle.Lifecycle
	tree *tree.Tree
	root *tree.Node

	manifest *manifest.Builder
	health   *health.Health
	guard    guard.Guard
	internal *grpc.Server   // platform port, gRPC: internal API + health
	platform *http.ServeMux // platform port, HTTP behind the guard: UI bundle
	public   []*publicPort

	mu      sync.Mutex
	owners  []any
	running bool
}

func newCore(ctx context.Context, o options, conf configState, cfg config.Backplane) *core {
	id := o.id.resolve(ctx, cfg)
	log := id.logger(o.log, cfg)
	// Libraries logging through log/slog (the OTel SDK among them) land in
	// the same stream, with identity and trace fields.
	slog.SetDefault(slog.New(xlog.NewSlogHandler(log)))

	t, root := tree.New(id.Service, log)

	c := &core{
		id: id, cfg: cfg, log: log, conf: conf,
		lc:       lifecycle.New(ctx, log, cfg.Shutdown.Timeout),
		tree:     t,
		root:     root,
		manifest: manifest.New(id.Service, id.Version),
		health:   health.New(log, healthInterval),
		guard:    guard.New(cfg.InternalSecret.Reveal()),
		platform: http.NewServeMux(),
	}
	c.internal = grpc.NewServer(serverOptions(c.guard.ServerOptions()...)...)
	hv1.RegisterHealthServer(c.internal, c.health.GRPC())
	c.manifest.Config(conf.Schema(), livePaths(conf.Live()))

	return c
}

// Name of the service.
func (c *core) Name() string { return c.id.Service }

// Identity of the instance.
func (c *core) Identity() Identity { return c.id }

// Log is the service logger; context-aware calls carry trace and span ids.
func (c *core) Log() *xlog.Logger { return c.log }

func (c *core) attach(owner any) {
	c.mu.Lock()
	c.owners = append(c.owners, owner)
	c.mu.Unlock()

	decl.Attach(owner, sink{c})
}

func (c *core) detach() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, o := range c.owners {
		decl.Detach(o)
	}
}

// declare runs fn unless the service is already running.
func (c *core) declare(what string, fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		c.log.Warn("declaration after Run ignored", xlog.String("what", what))

		return
	}

	fn()
}

// start marks the service running; false when it already was.
func (c *core) start() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return false
	}

	c.running = true

	return true
}

// serverOptions: telemetry, then extra.
func serverOptions(extra ...grpc.ServerOption) []grpc.ServerOption {
	return append([]grpc.ServerOption{grpc.StatsHandler(otelgrpc.NewServerHandler())}, extra...)
}

// platformHandler: probes open, everything else behind the guard.
func (c *core) platformHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(probesPath, c.health.HTTP())
	mux.Handle("/", c.guard.HTTP(c.platform))

	return mux
}

func livePaths(paths []xconf.Path) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.Join(p, ".")
	}

	return out
}
