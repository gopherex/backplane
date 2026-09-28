// Package route describes a service's public routes for Envoy.
//
// Managed routes are served by the SDK: Service.GRPC, Service.HTTP,
// Service.GraphQL and the wsproto package. Declarative routes (route.GRPC,
// route.HTTP, route.GraphQL, route.WSProto passed to Service.Route)
// announce what the author serves on their own listener. Every function
// takes its own option interface: an option that makes no sense for it does
// not compile.
//
// HTTP prefixes are normalized to end with "/": "/api" serves "/api/..." in
// Go and at Envoy alike. A route matches by prefix and, with Host, by host
// too; two routes with the same host and prefix on one port are an error.
//
// Envoy policy (Timeout, IdleTimeout, Retry, CORS, MaxRequestBytes) applies
// to every route and lands in the manifest; the SDK itself does not enforce
// it. Interceptors, StreamInterceptors and Middleware wrap only the
// registration they are passed to.
package route

import (
	"errors"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

//nolint:gochecknoinits // installs the private accessors for the SDK core
func init() {
	link.Routes.Decl = func(d any) (*backplanev1.Route, error) {
		decl, _ := d.(Decl)
		if decl.r == nil && decl.err == nil {
			return nil, errZeroDecl
		}

		return decl.r, decl.err
	}
	link.Routes.GRPC = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]GRPCOption)
		for _, o := range list {
			o.applyGRPC(&m)
		}

		return m
	}
	link.Routes.HTTP = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]HTTPOption)
		for _, o := range list {
			o.applyHTTP(&m)
		}

		return m
	}
	link.Routes.WS = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]WSProtoOption)
		for _, o := range list {
			o.applyWS(&m)
		}

		return m
	}
}

var errZeroDecl = errors.New("route: zero Decl: use route.GRPC, HTTP, GraphQL or WSProto")

// Option interfaces, one per function.
type (
	// GRPCOption configures Service.GRPC.
	GRPCOption interface{ applyGRPC(m *routes.Managed) }
	// HTTPOption configures Service.HTTP and Service.GraphQL.
	HTTPOption interface{ applyHTTP(m *routes.Managed) }
	// WSProtoOption configures wsproto.Serve.
	WSProtoOption interface{ applyWS(m *routes.Managed) }
	// GRPCDeclOption configures route.GRPC.
	GRPCDeclOption interface{ applyGRPCDecl(r *backplanev1.Route) }
	// HTTPDeclOption configures route.HTTP.
	HTTPDeclOption interface{ applyHTTPDecl(r *backplanev1.Route) }
	// DeclOption configures route.GraphQL and route.WSProto.
	DeclOption interface{ applyDecl(r *backplanev1.Route) }
)

type listen string

func (l listen) applyGRPC(m *routes.Managed) { m.Listen = string(l) }
func (l listen) applyHTTP(m *routes.Managed) { m.Listen = string(l) }
func (l listen) applyWS(m *routes.Managed)   { m.Listen = string(l) }

// ListenOption applies to managed routes.
type ListenOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
}

// Listen serves a managed route on its own listener instead of the shared
// public port; routes with the same address share one server.
func Listen(addr string) ListenOption { return listen(addr) }

type host string

func (h host) applyGRPC(m *routes.Managed)        { m.Host = string(h) }
func (h host) applyHTTP(m *routes.Managed)        { m.Host = string(h) }
func (h host) applyWS(m *routes.Managed)          { m.Host = string(h) }
func (h host) applyGRPCDecl(r *backplanev1.Route) { h.applyDecl(r) }
func (h host) applyHTTPDecl(r *backplanev1.Route) { h.applyDecl(r) }
func (h host) applyDecl(r *backplanev1.Route)     { r.Host = string(h) }

// HostOption applies to every route.
type HostOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
	GRPCDeclOption
	HTTPDeclOption
	DeclOption
}

// Host matches the route by Host header in addition to its prefix: an exact
// host ("api.example.com") or a wildcard ("*.example.com"). Managed HTTP
// routes with one prefix and different hosts share a port.
func Host(h string) HostOption { return host(h) }

type port uint32

func (p port) applyGRPCDecl(r *backplanev1.Route) { r.Port = uint32(p) }
func (p port) applyHTTPDecl(r *backplanev1.Route) { r.Port = uint32(p) }
func (p port) applyDecl(r *backplanev1.Route)     { r.Port = uint32(p) }

// PortOption applies to declarative routes.
type PortOption interface {
	GRPCDeclOption
	HTTPDeclOption
	DeclOption
}

// Port is where the author serves a declarative route; 0 means the port
// registered in Consul.
func Port(p uint16) PortOption { return port(p) }

type transcode struct{}

func (transcode) applyGRPC(m *routes.Managed) { m.Transcode = true }
func (transcode) applyGRPCDecl(r *backplanev1.Route) {
	r.Kind = backplanev1.RouteKind_ROUTE_KIND_CONNECT
}

// TranscodeOption applies to gRPC routes.
type TranscodeOption interface {
	GRPCOption
	GRPCDeclOption
}

// Transcode asks Envoy to add gRPC-Web and REST-JSON transcoding (Connect).
func Transcode() TranscodeOption { return transcode{} }

type openAPI []byte

func (o openAPI) applyHTTP(m *routes.Managed) { m.OpenAPI = o }
func (o openAPI) applyHTTPDecl(r *backplanev1.Route) {
	r.Schema = &backplanev1.Route_Openapi{Openapi: o}
}

// OpenAPIOption applies to HTTP routes.
type OpenAPIOption interface {
	HTTPOption
	HTTPDeclOption
}

// OpenAPI attaches the OpenAPI document of an HTTP route.
func OpenAPI(spec []byte) OpenAPIOption { return openAPI(spec) }

type descriptors []byte

func (d descriptors) applyGRPCDecl(r *backplanev1.Route) {
	r.Schema = &backplanev1.Route_Descriptors{Descriptors: d}
}

// Descriptors attaches a serialized FileDescriptorSet to a declarative gRPC
// route; managed routes derive descriptors from registration.
func Descriptors(fds []byte) GRPCDeclOption { return descriptors(fds) }

// Decl is a declarative route: the author serves it, the SDK announces it.
// Build one with GRPC, HTTP, GraphQL or WSProto.
type Decl struct {
	r   *backplanev1.Route
	err error
}

// GRPC declares a gRPC service (full name) the author serves.
func GRPC(service string, opts ...GRPCDeclOption) Decl {
	r := &backplanev1.Route{
		Kind:     backplanev1.RouteKind_ROUTE_KIND_GRPC,
		Prefix:   "/" + service + "/",
		Services: []string{service},
	}
	for _, o := range opts {
		o.applyGRPCDecl(r)
	}

	return Decl{r: r}
}

// HTTP declares an HTTP prefix the author serves.
func HTTP(prefix string, opts ...HTTPDeclOption) Decl {
	r, err := prefixed(backplanev1.RouteKind_ROUTE_KIND_HTTP, prefix)
	for _, o := range opts {
		o.applyHTTPDecl(r)
	}

	return Decl{r: r, err: err}
}

// GraphQL declares a GraphQL endpoint the author serves, with its
// introspection result (JSON); nil when there is none.
func GraphQL(prefix string, introspection []byte, opts ...DeclOption) Decl {
	r, err := prefixed(backplanev1.RouteKind_ROUTE_KIND_GRAPHQL, prefix)
	if introspection != nil {
		r.Schema = &backplanev1.Route_Graphql{Graphql: introspection}
	}

	for _, o := range opts {
		o.applyDecl(r)
	}

	return Decl{r: r, err: err}
}

// WSProto declares a ws-proto endpoint the author serves, with its
// serialized FileDescriptorSet; nil when there is none.
func WSProto(prefix string, descriptors []byte, opts ...DeclOption) Decl {
	r, err := prefixed(backplanev1.RouteKind_ROUTE_KIND_WS_PROTO, prefix)
	if descriptors != nil {
		r.Schema = &backplanev1.Route_Descriptors{Descriptors: descriptors}
	}

	for _, o := range opts {
		o.applyDecl(r)
	}

	return Decl{r: r, err: err}
}

func prefixed(kind backplanev1.RouteKind, prefix string) (*backplanev1.Route, error) {
	p, err := routes.Prefix(prefix)

	//nolint:wrapcheck // routes.Prefix speaks for this package ("route: bad path prefix")
	return &backplanev1.Route{Kind: kind, Prefix: p}, err
}
