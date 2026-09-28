// Package route describes a service's public routes for Envoy.
//
// Managed routes are served by the SDK: Service.GRPC, Service.HTTP and the
// wsproto package. A GraphQL endpoint is the author's own handler served
// with Service.HTTP and route.AsGraphQL. Declarative routes (route.GRPC,
// route.HTTP, route.GraphQL, route.WSProto) announce what the author serves
// on their own listener. Every function takes its own option type, so an
// option that makes no sense for it does not compile.
package route

import backplanev1 "github.com/gopherex/backplane/backplanepb/v1"

// Kind of a route.
type Kind = backplanev1.RouteKind

// Route kinds.
const (
	KindHTTP    = backplanev1.RouteKind_ROUTE_KIND_HTTP
	KindGRPC    = backplanev1.RouteKind_ROUTE_KIND_GRPC
	KindConnect = backplanev1.RouteKind_ROUTE_KIND_CONNECT
	KindWSProto = backplanev1.RouteKind_ROUTE_KIND_WS_PROTO
	KindGraphQL = backplanev1.RouteKind_ROUTE_KIND_GRAPHQL
)

// Schema attached to a route; at most one field is set.
type Schema struct {
	OpenAPI     []byte // HTTP
	Descriptors []byte // gRPC, Connect, ws-proto: serialized FileDescriptorSet
	GraphQL     []byte // GraphQL: introspection result as JSON
}

// GRPCSpec is what Service.GRPC receives after applying options.
type GRPCSpec struct {
	Listen    string
	Host      string
	Transcode bool
}

// HTTPSpec is what Service.HTTP receives after applying options.
type HTTPSpec struct {
	Listen string
	Host   string
	Kind   Kind
	Schema Schema
}

// Decl is a declarative route: the author serves it, the SDK announces it.
type Decl struct {
	Kind    Kind
	Service string // gRPC and Connect: full service name
	Prefix  string // HTTP, GraphQL, ws-proto: path prefix
	Host    string
	Port    uint32
	Schema  Schema
}

// Option types, one per function.
type (
	GRPCOption interface{ applyGRPC(spec *GRPCSpec) }
	HTTPOption interface{ applyHTTP(spec *HTTPSpec) }
	DeclOption interface{ applyDecl(decl *Decl) }
)

type listen string

func (l listen) applyGRPC(s *GRPCSpec) { s.Listen = string(l) }
func (l listen) applyHTTP(s *HTTPSpec) { s.Listen = string(l) }

// ListenOption applies to managed routes.
type ListenOption interface {
	GRPCOption
	HTTPOption
}

// Listen serves a managed route on its own listener instead of the shared
// public port; routes with the same address share one server.
func Listen(addr string) ListenOption { return listen(addr) }

type host string

func (h host) applyGRPC(s *GRPCSpec) { s.Host = string(h) }
func (h host) applyHTTP(s *HTTPSpec) { s.Host = string(h) }
func (h host) applyDecl(d *Decl)     { d.Host = string(h) }

// HostOption applies everywhere.
type HostOption interface {
	GRPCOption
	HTTPOption
	DeclOption
}

// Host matches the route by host header instead of path.
func Host(h string) HostOption { return host(h) }

type openAPI []byte

func (o openAPI) applyHTTP(s *HTTPSpec) { s.Schema = Schema{OpenAPI: o} }
func (o openAPI) applyDecl(d *Decl)     { d.Schema = Schema{OpenAPI: o} }

// SchemaOption applies to managed HTTP and declarative routes.
type SchemaOption interface {
	HTTPOption
	DeclOption
}

// OpenAPI attaches the OpenAPI document of an HTTP route.
func OpenAPI(spec []byte) SchemaOption { return openAPI(spec) }

type descriptors []byte

func (d descriptors) applyDecl(r *Decl) { r.Schema = Schema{Descriptors: d} }

// Descriptors attaches a serialized FileDescriptorSet to a declarative route;
// managed routes derive descriptors from registration.
func Descriptors(fds []byte) DeclOption { return descriptors(fds) }

type introspection []byte

func (i introspection) applyDecl(r *Decl) { r.Schema = Schema{GraphQL: i} }

// Introspection attaches a GraphQL introspection result (JSON) to a
// declarative GraphQL route.
func Introspection(json []byte) DeclOption { return introspection(json) }

type port uint32

func (p port) applyDecl(d *Decl) { d.Port = uint32(p) }

// Port is where the author serves a declarative route; 0 means the port
// registered in Consul.
func Port(p uint16) DeclOption { return port(p) }

type transcode struct{}

func (transcode) applyGRPC(s *GRPCSpec) { s.Transcode = true }
func (transcode) applyDecl(d *Decl) {
	if d.Kind == KindGRPC {
		d.Kind = KindConnect
	}
}

// TranscodeOption applies to gRPC routes.
type TranscodeOption interface {
	GRPCOption
	DeclOption
}

// Transcode asks Envoy to add gRPC-Web and REST-JSON transcoding (Connect).
func Transcode() TranscodeOption { return transcode{} }

// kinded sets the kind and schema of a managed HTTP route.
type kinded struct {
	kind   Kind
	schema Schema
}

func (k kinded) applyHTTP(s *HTTPSpec) { s.Kind, s.Schema = k.kind, k.schema }

// AsGraphQL marks a managed HTTP route as GraphQL: the author's handler,
// announced with its introspection result (JSON) for Envoy and the console.
func AsGraphQL(introspectionJSON []byte) HTTPOption {
	return kinded{kind: KindGraphQL, schema: Schema{GraphQL: introspectionJSON}}
}

// AsWSProto marks a managed HTTP route as ws-proto with its descriptors.
func AsWSProto(fds []byte) HTTPOption {
	return kinded{kind: KindWSProto, schema: Schema{Descriptors: fds}}
}

func declare(kind Kind, service, prefix string, opts []DeclOption) Decl {
	d := Decl{Kind: kind, Service: service, Prefix: prefix}
	for _, o := range opts {
		o.applyDecl(&d)
	}

	return d
}

// GRPC declares a gRPC service the author serves.
func GRPC(service string, opts ...DeclOption) Decl { return declare(KindGRPC, service, "", opts) }

// HTTP declares an HTTP prefix the author serves.
func HTTP(prefix string, opts ...DeclOption) Decl { return declare(KindHTTP, "", prefix, opts) }

// GraphQL declares a GraphQL endpoint the author serves.
func GraphQL(prefix string, opts ...DeclOption) Decl {
	return declare(KindGraphQL, "", prefix, opts)
}

// WSProto declares a ws-proto endpoint the author serves.
func WSProto(prefix string, opts ...DeclOption) Decl {
	return declare(KindWSProto, "", prefix, opts)
}

// NewGRPCSpec applies gRPC options.
func NewGRPCSpec(opts ...GRPCOption) GRPCSpec {
	var s GRPCSpec
	for _, o := range opts {
		o.applyGRPC(&s)
	}

	return s
}

// NewHTTPSpec applies HTTP options; the kind defaults to HTTP.
func NewHTTPSpec(opts ...HTTPOption) HTTPSpec {
	s := HTTPSpec{Kind: KindHTTP}
	for _, o := range opts {
		o.applyHTTP(&s)
	}

	return s
}

// Kind of a managed gRPC route: Connect when transcoding.
func (s GRPCSpec) Kind() Kind {
	if s.Transcode {
		return KindConnect
	}

	return KindGRPC
}

// Proto converts a declarative route.
func (d Decl) Proto() *backplanev1.Route {
	r := &backplanev1.Route{Kind: d.Kind, Port: d.Port}
	switch {
	case d.Host != "":
		r.Match = &backplanev1.Route_Host{Host: d.Host}
	case d.Service != "":
		r.Match = &backplanev1.Route_Prefix{Prefix: "/" + d.Service + "/"}
	default:
		r.Match = &backplanev1.Route_Prefix{Prefix: d.Prefix}
	}

	switch {
	case d.Schema.OpenAPI != nil:
		r.Schema = &backplanev1.Route_Openapi{Openapi: d.Schema.OpenAPI}
	case d.Schema.Descriptors != nil:
		r.Schema = &backplanev1.Route_Descriptors{Descriptors: d.Schema.Descriptors}
	case d.Schema.GraphQL != nil:
		r.Schema = &backplanev1.Route_Graphql{Graphql: d.Schema.GraphQL}
	}

	return r
}

// Proto converts a managed HTTP route served under prefix on port.
func (s HTTPSpec) Proto(prefix string, port uint32) *backplanev1.Route {
	return Decl{Kind: s.Kind, Prefix: prefix, Host: s.Host, Port: port, Schema: s.Schema}.Proto()
}
