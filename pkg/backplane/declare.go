package backplane

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/gopherex/xprobe/pkg/probe"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

//nolint:gochecknoinits // installs the private accessor for the wsproto package
func init() {
	link.MountWS = func(svc any, prefix string, h http.Handler, services []string, opts any) {
		m, ok := svc.(interface {
			mountWS(prefix string, h http.Handler, services []string, spec routes.Managed)
		})
		if !ok {
			panic("backplane: wsproto.Serve needs a *backplane.Service")
		}

		m.mountWS(prefix, h, services, link.Routes.HTTP(opts))
	}
}

// Declaration errors, reported by Run.
var (
	errPrefixTaken    = errors.New("prefix served twice")
	errConsolePackage = errors.New("must be in proto package")
)

// Route announces a route the author serves themselves.
func (c *core) Route(d route.Decl) {
	c.declare("route", func() {
		r, err := link.Routes.Decl(d)
		if err != nil {
			c.env.Manifest.Fail(err)

			return
		}

		c.env.Manifest.Route(r)
	})
}

// GRPC serves gRPC services on a public port: one route per registered
// service, descriptors derived from the registration. Registering the same
// service twice on one port is a Run error.
func (c *core) GRPC(register func(r grpc.ServiceRegistrar), opts ...route.GRPCOption) {
	spec := link.Routes.GRPC(opts)

	c.declare("gRPC routes", func() {
		p := c.port(spec.Listen)

		services, err := manifest.Register(p.grpcServer(), register)
		if err != nil {
			c.env.Manifest.Fail(err)
		}

		kind := backplanev1.RouteKind_ROUTE_KIND_GRPC
		if spec.Transcode {
			kind = backplanev1.RouteKind_ROUTE_KIND_CONNECT
		}

		for _, name := range services {
			r := &backplanev1.Route{Kind: kind, Port: uint32(p.port), Services: []string{name}}
			setMatch(r, spec.Host, "/"+name+"/")
			c.env.Manifest.Route(r)
		}
	})
}

// HTTP serves h under prefix on a public port. The prefix is normalized to
// end with "/" ("/api" serves "/api/..."); a duplicate is a Run error.
func (c *core) HTTP(prefix string, h http.Handler, opts ...route.HTTPOption) {
	c.mountHTTP("HTTP route", backplanev1.RouteKind_ROUTE_KIND_HTTP, prefix, h, link.Routes.HTTP(opts), nil)
}

// GraphQL serves the author's GraphQL handler under prefix on a public
// port, announced with its introspection result (JSON; nil when none).
func (c *core) GraphQL(prefix string, h http.Handler, introspection []byte, opts ...route.HTTPOption) {
	var schema *backplanev1.Route
	if introspection != nil {
		schema = &backplanev1.Route{Schema: &backplanev1.Route_Graphql{Graphql: introspection}}
	}

	c.mountHTTP("GraphQL route", backplanev1.RouteKind_ROUTE_KIND_GRAPHQL, prefix, h, link.Routes.HTTP(opts), schema)
}

func (c *core) mountWS(prefix string, h http.Handler, services []string, spec routes.Managed) {
	c.mountHTTP("ws-proto route", backplanev1.RouteKind_ROUTE_KIND_WS_PROTO, prefix, h, spec,
		&backplanev1.Route{Services: services})
}

// mountHTTP serves h and announces it; extra carries the kind's schema and
// services.
func (c *core) mountHTTP(
	what string, kind backplanev1.RouteKind, prefix string, h http.Handler, spec routes.Managed,
	extra *backplanev1.Route,
) {
	c.declare(what, func() {
		p, err := routes.Prefix(prefix)
		if err != nil {
			c.env.Manifest.Fail(err)

			return
		}

		port := c.port(spec.Listen)
		if !port.handle(p) {
			c.env.Manifest.Fail(fmt.Errorf("%s %s: %w on %s", what, p, errPrefixTaken, port.addr))

			return
		}

		port.mux().Handle(p, h)

		r := &backplanev1.Route{Kind: kind, Port: uint32(port.port)}
		setMatch(r, spec.Host, p)

		if spec.OpenAPI != nil {
			r.Schema = &backplanev1.Route_Openapi{Openapi: spec.OpenAPI}
		}

		if extra != nil {
			r.Services = extra.GetServices()
			if extra.GetSchema() != nil {
				r.Schema = extra.GetSchema()
			}
		}

		c.env.Manifest.Route(r)
	})
}

// setMatch matches r by path prefix and, when given, host.
func setMatch(r *backplanev1.Route, host, prefix string) {
	r.Prefix, r.Host = prefix, host
}

// Internal registers the internal API: gRPC on the platform port for the
// service's own console plugin, behind the internal secret, served only
// while the author's tree is up. Services must live in the proto package
// <service>.console.v1.
func (c *core) Internal(register func(r grpc.ServiceRegistrar)) {
	c.declare("internal API", func() {
		services, err := manifest.Register(c.internal, register)
		if err != nil {
			c.env.Manifest.Fail(err)
		}

		pkg := strings.ReplaceAll(c.id.Service, "-", "_") + ".console.v1."
		for _, name := range services {
			if !strings.HasPrefix(name, pkg) {
				c.env.Manifest.Fail(fmt.Errorf("internal API %s: %w %s", name, errConsolePackage, strings.TrimSuffix(pkg, ".")))
			}
		}

		c.env.Manifest.Internal(services)
	})
}

// UI declares the console plugin bundle, served at /_backplane/ui/ behind the
// internal secret.
func (c *core) UI(bundle fs.FS) {
	c.declare("UI", func() {
		c.env.Manifest.UI(bundle)
		c.platform.Handle(uiPath, http.StripPrefix(uiPath, http.FileServerFS(bundle)))
	})
}

// LivenessProbe adds a liveness probe. Keep dependencies out of it: a dead
// database is no reason to restart the process.
func (c *core) LivenessProbe(p probe.Probe) {
	c.declare("liveness probe", func() { c.health.Add(health.Live, p) })
}

// ReadinessProbe adds a readiness probe; readiness drives grpc.health.v1
// and so the Consul check. Required dependencies are readiness probes
// already.
func (c *core) ReadinessProbe(p probe.Probe) {
	c.declare("readiness probe", func() { c.health.Add(health.Ready, p) })
}

// StartupProbe adds a startup probe.
func (c *core) StartupProbe(p probe.Probe) {
	c.declare("startup probe", func() { c.health.Add(health.Startup, p) })
}

// publicPort is one managed public listener: gRPC and HTTP share it.
type publicPort struct {
	addr     string
	port     uint16
	grpc     *grpc.Server
	http     *http.ServeMux
	prefixes map[string]bool
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

	p := &publicPort{addr: listen, prefixes: map[string]bool{}}

	n, err := portOf(listen)
	if err != nil {
		c.env.Manifest.Fail(err)
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

// handle claims prefix; false when it is taken.
func (p *publicPort) handle(prefix string) bool {
	if p.prefixes[prefix] {
		return false
	}

	p.prefixes[prefix] = true

	return true
}

// handler is the traced HTTP side of the port, or nil without HTTP routes.
func (p *publicPort) handler() http.Handler {
	if p.http == nil {
		return nil
	}

	return otelhttp.NewHandler(p.http, "public"+p.addr)
}

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
