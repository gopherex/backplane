package route_test

import (
	"testing"

	"github.com/gopherex/backplane/pkg/backplane/route"
)

func TestDeclarative(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		decl      route.Decl
		kind      route.Kind
		prefix    string
		host      string
		port      uint32
		hasSchema bool
	}{
		"grpc":      {route.GRPC("hello.v1.Hello", route.Port(9000)), route.KindGRPC, "/hello.v1.Hello/", "", 9000, false},
		"connect":   {route.GRPC("hello.v1.Hello", route.Transcode(), route.Descriptors([]byte{1})), route.KindConnect, "/hello.v1.Hello/", "", 0, true},
		"http host": {route.HTTP("/x/", route.Host("api.example.com"), route.OpenAPI([]byte("{}"))), route.KindHTTP, "", "api.example.com", 0, true},
		"graphql":   {route.GraphQL("/graphql", route.Introspection([]byte("{}"))), route.KindGraphQL, "/graphql", "", 0, true},
		"wsproto":   {route.WSProto("/ws/"), route.KindWSProto, "/ws/", "", 0, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := c.decl.Proto()
			if r.GetKind() != c.kind || r.GetPrefix() != c.prefix || r.GetHost() != c.host || r.GetPort() != c.port {
				t.Fatalf("got %v", r)
			}

			if (r.GetSchema() != nil) != c.hasSchema {
				t.Fatalf("schema presence: %v", r.GetSchema())
			}
		})
	}
}

func TestManagedSpecs(t *testing.T) {
	t.Parallel()

	if k := route.NewGRPCSpec(route.Transcode(), route.Listen(":9000")).Kind(); k != route.KindConnect {
		t.Fatalf("grpc kind %v", k)
	}

	spec := route.NewHTTPSpec(route.AsGraphQL([]byte("{}")))
	if spec.Kind != route.KindGraphQL || spec.Proto("/graphql", 8080).GetGraphql() == nil {
		t.Fatalf("graphql spec %+v", spec)
	}

	if route.NewHTTPSpec().Kind != route.KindHTTP {
		t.Fatal("default kind")
	}
}
