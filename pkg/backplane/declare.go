package backplane

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/gopherex/xprobe/pkg/probe"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

// Route announces a route the author serves themselves.
func (c *core) Route(r route.Decl) {
	c.declare("route", func() { c.manifest.Route(r.Proto()) })
}

// GRPC serves gRPC services on a public port; one route per registered
// service, descriptors derived from the registration.
func (c *core) GRPC(register func(r grpc.ServiceRegistrar), opts ...route.GRPCOption) {
	spec := route.NewGRPCSpec(opts...)

	c.declare("grpc", func() {
		p := c.port(spec.Listen)
		c.manifest.GRPC(manifest.Services(p.grpcServer(), register), spec.Kind(), uint32(p.port), spec.Host)
	})
}

// HTTP serves h under prefix on a public port.
func (c *core) HTTP(prefix string, h http.Handler, opts ...route.HTTPOption) {
	spec := route.NewHTTPSpec(opts...)

	c.declare("http", func() {
		p := c.port(spec.Listen)
		p.mux().Handle(prefix, h)
		c.manifest.Route(spec.Proto(prefix, uint32(p.port)))
	})
}

// Internal registers the internal API: gRPC on the platform port for the
// service's own console plugin, behind the internal secret.
func (c *core) Internal(register func(r grpc.ServiceRegistrar)) {
	c.declare("internal", func() { c.manifest.Internal(manifest.Services(c.internal, register)) })
}

// UI declares the console plugin bundle, served at /_backplane/ui/ behind the
// internal secret.
func (c *core) UI(bundle fs.FS) {
	c.declare("ui", func() {
		c.manifest.UI(bundle)
		c.platform.Handle(uiPath, http.StripPrefix(uiPath, http.FileServerFS(bundle)))
	})
}

// Live adds a liveness probe.
func (c *core) Live(p probe.Probe) { c.declare("live", func() { c.health.Add(health.Live, p) }) }

// Ready adds a readiness probe; readiness drives grpc.health.v1 and so the
// Consul check. Required dependencies are readiness probes already.
func (c *core) Ready(p probe.Probe) { c.declare("ready", func() { c.health.Add(health.Ready, p) }) }

// Startup adds a startup probe.
func (c *core) Startup(p probe.Probe) {
	c.declare("startup", func() { c.health.Add(health.Startup, p) })
}

// sink receives declarations of the hook, activity and event packages.
type sink struct{ c *core }

func (k sink) Hook(h *backplanev1.Hook) { k.c.declare("hook", func() { k.c.manifest.Hook(h) }) }

func (k sink) Activity(a *backplanev1.Activity) {
	k.c.declare("activity", func() { k.c.manifest.Activity(a) })
}

func (k sink) Event(e *backplanev1.Event) { k.c.declare("event", func() { k.c.manifest.Event(e) }) }

// publicPort is one managed public listener: gRPC and HTTP share it.
type publicPort struct {
	addr string
	port uint16
	grpc *grpc.Server
	http *http.ServeMux
}

// port returns the public port for listen ("" = the default public port).
func (c *core) port(listen string) *publicPort {
	if listen == "" {
		listen = listenAddr(c.cfg.PublicPort)
	}

	for _, p := range c.public {
		if p.addr == listen {
			return p
		}
	}

	p := &publicPort{addr: listen}

	n, err := portOf(listen)
	if err != nil {
		c.manifest.Fail(err)
	}

	p.port = n
	c.public = append(c.public, p)

	return p
}

// primaryPort is registered in Consul: the default public port when used,
// else the first declared one, else 0.
func (c *core) primaryPort() uint16 {
	for _, p := range c.public {
		if p.addr == listenAddr(c.cfg.PublicPort) {
			return p.port
		}
	}

	if len(c.public) > 0 {
		return c.public[0].port
	}

	return 0
}

func (p *publicPort) grpcServer() *grpc.Server {
	if p.grpc == nil {
		p.grpc = grpc.NewServer(serverOptions()...)
	}

	return p.grpc
}

func (p *publicPort) mux() *http.ServeMux {
	if p.http == nil {
		p.http = http.NewServeMux()
	}

	return p.http
}

// handler is the traced HTTP side of the port, or nil without HTTP routes.
func (p *publicPort) handler() http.Handler {
	if p.http == nil {
		return nil
	}

	return otelhttp.NewHandler(p.http, "public"+p.addr)
}

func listenAddr(port int64) string { return ":" + strconv.FormatInt(port, 10) }

func portOf(listen string) (uint16, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("listen %q: %w", listen, err)
	}

	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("listen %q: %w", listen, err)
	}

	return uint16(n), nil
}
