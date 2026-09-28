package route_test

import (
	"errors"
	"slices"
	"testing"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

func TestDeclarative(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		decl      route.Decl
		kind      backplanev1.RouteKind
		prefix    string
		host      string
		port      uint32
		hasSchema bool
	}{
		"grpc": {
			route.GRPC("hello.v1.Hello", route.Port(9000)),
			backplanev1.RouteKind_ROUTE_KIND_GRPC, "/hello.v1.Hello/", "", 9000, false,
		},
		"connect": {
			route.GRPC("hello.v1.Hello", route.Transcode(), route.Descriptors([]byte{1})),
			backplanev1.RouteKind_ROUTE_KIND_CONNECT, "/hello.v1.Hello/", "", 0, true,
		},
		"http host": {
			route.HTTP("/x/", route.Host("api.example.com"), route.OpenAPI([]byte("{}"))),
			backplanev1.RouteKind_ROUTE_KIND_HTTP, "", "api.example.com", 0, true,
		},
		"http normalized": {
			route.HTTP("/api", route.Port(8081)),
			backplanev1.RouteKind_ROUTE_KIND_HTTP, "/api/", "", 8081, false,
		},
		"graphql": {
			route.GraphQL("/graphql", []byte("{}")),
			backplanev1.RouteKind_ROUTE_KIND_GRAPHQL, "/graphql/", "", 0, true,
		},
		"graphql no introspection": {
			route.GraphQL("/graphql/", nil, route.Host("gql.example.com")),
			backplanev1.RouteKind_ROUTE_KIND_GRAPHQL, "", "gql.example.com", 0, false,
		},
		"wsproto": {
			route.WSProto("/ws", nil),
			backplanev1.RouteKind_ROUTE_KIND_WS_PROTO, "/ws/", "", 0, false,
		},
		"wsproto descriptors": {
			route.WSProto("/ws/", []byte{1}, route.Port(7000)),
			backplanev1.RouteKind_ROUTE_KIND_WS_PROTO, "/ws/", "", 7000, true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r, err := link.Routes.Decl(c.decl)
			if err != nil {
				t.Fatal(err)
			}

			if r.GetKind() != c.kind || r.GetPrefix() != c.prefix || r.GetHost() != c.host || r.GetPort() != c.port {
				t.Fatalf("got %v", r)
			}

			if (r.GetSchema() != nil) != c.hasSchema {
				t.Fatalf("schema presence: %v", r.GetSchema())
			}
		})
	}
}

func TestGRPCDeclNamesService(t *testing.T) {
	t.Parallel()

	r, err := link.Routes.Decl(route.GRPC("hello.v1.Hello"))
	if err != nil || !slices.Equal(r.GetServices(), []string{"hello.v1.Hello"}) {
		t.Fatalf("services %v err %v", r.GetServices(), err)
	}
}

func TestBadPrefixIsDeclError(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]route.Decl{
		"http relative":     route.HTTP("api"),
		"http method":       route.HTTP("GET /x"),
		"http wildcard":     route.HTTP("/x/{id}"),
		"graphql relative":  route.GraphQL("graphql", nil),
		"wsproto wildcard":  route.WSProto("/ws/{x}", nil),
		"http with options": route.HTTP("api", route.Port(1), route.Host("h")),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := link.Routes.Decl(d); !errors.Is(err, routes.ErrPrefix) {
				t.Fatalf("want ErrPrefix, got %v", err)
			}
		})
	}
}

func TestZeroDeclIsError(t *testing.T) {
	t.Parallel()

	if _, err := link.Routes.Decl(route.Decl{}); err == nil {
		t.Fatal("zero Decl accepted")
	}
}

func TestPrefix(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in, want string
		ok       bool
	}{
		"slash added": {"/api", "/api/", true},
		"kept":        {"/api/", "/api/", true},
		"root":        {"/", "/", true},
		"nested":      {"/a/b", "/a/b/", true},
		"relative":    {"api", "", false},
		"empty":       {"", "", false},
		"method":      {"GET /x", "", false},
		"tab":         {"/x\t", "", false},
		"wildcard":    {"/x/{id}", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := routes.Prefix(c.in)
			if c.ok != (err == nil) || got != c.want {
				t.Fatalf("Prefix(%q) = %q, %v", c.in, got, err)
			}

			if err != nil && !errors.Is(err, routes.ErrPrefix) {
				t.Fatalf("error does not wrap ErrPrefix: %v", err)
			}
		})
	}
}

func TestManagedOptions(t *testing.T) {
	t.Parallel()

	g := link.Routes.GRPC([]route.GRPCOption{route.Transcode(), route.Listen(":9000"), route.Host("api.example.com")})
	if g.Listen != ":9000" || g.Host != "api.example.com" || !g.Transcode || g.OpenAPI != nil {
		t.Fatalf("grpc: %+v", g)
	}

	if d := link.Routes.GRPC([]route.GRPCOption(nil)); d.Transcode || d.Listen != "" || d.Host != "" {
		t.Fatalf("grpc defaults: %+v", d)
	}

	h := link.Routes.HTTP([]route.HTTPOption{route.OpenAPI([]byte("{}")), route.Listen(":8081"), route.Host("h")})
	if h.Listen != ":8081" || h.Host != "h" || string(h.OpenAPI) != "{}" || h.Transcode {
		t.Fatalf("http: %+v", h)
	}
}

func TestOrigins(t *testing.T) {
	t.Parallel()

	var zero route.Origins
	if zero.IsAny() || len(zero.Patterns()) != 0 {
		t.Fatalf("zero origins: %v %v", zero.IsAny(), zero.Patterns())
	}

	if !route.AnyOrigin().IsAny() {
		t.Fatal("AnyOrigin")
	}

	o := route.AllowOrigins("app.example.com", "*.example.com")
	if o.IsAny() || !slices.Equal(o.Patterns(), []string{"app.example.com", "*.example.com"}) {
		t.Fatalf("patterns: %v", o.Patterns())
	}

	o.Patterns()[0] = "changed"
	if o.Patterns()[0] != "app.example.com" {
		t.Fatal("Patterns exposes the internal slice")
	}
}
