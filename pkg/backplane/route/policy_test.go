package route_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

func wantPolicy() *backplanev1.RoutePolicy {
	return &backplanev1.RoutePolicy{
		Timeout:     durationpb.New(time.Minute),
		IdleTimeout: durationpb.New(10 * time.Second),
		Retry: &backplanev1.RetryPolicy{
			Attempts: 3, PerTryTimeout: durationpb.New(time.Second), RetryOn: []string{"5xx", "reset"},
		},
		Cors: &backplanev1.Cors{
			Origins: []string{"https://app.example.com"}, Methods: []string{"GET"}, Headers: []string{"X-A"},
			ExposeHeaders: []string{"X-B"}, Credentials: true, MaxAge: durationpb.New(time.Hour),
		},
		MaxRequestBytes: 1 << 20,
	}
}

func cors() route.CORSPolicy {
	return route.CORSPolicy{
		Origins: []string{"https://app.example.com"}, Methods: []string{"GET"}, Headers: []string{"X-A"},
		ExposeHeaders: []string{"X-B"}, Credentials: true, MaxAge: time.Hour,
	}
}

// Every policy option lands in the route's policy, managed and declarative.
func TestPolicyOptions(t *testing.T) {
	t.Parallel()

	want := wantPolicy()

	g := link.Routes.GRPC([]route.GRPCOption{
		route.Timeout(time.Minute), route.IdleTimeout(10 * time.Second),
		route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1 << 20),
	})
	if !proto.Equal(g.Policy, want) {
		t.Fatalf("grpc managed policy: %v", g.Policy)
	}

	h := link.Routes.HTTP([]route.HTTPOption{
		route.Timeout(time.Minute), route.IdleTimeout(10 * time.Second),
		route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1 << 20),
	})
	if !proto.Equal(h.Policy, want) {
		t.Fatalf("http managed policy: %v", h.Policy)
	}

	w := link.Routes.WS([]route.WSProtoOption{route.Timeout(time.Minute), route.Listen(":1"), route.Host("h")})
	if w.Policy.GetTimeout().AsDuration() != time.Minute || w.Listen != ":1" || w.Host != "h" {
		t.Fatalf("ws managed: %+v", w)
	}

	decls := map[string]route.Decl{
		"grpc": route.GRPC("a.v1.A", route.Timeout(time.Minute), route.IdleTimeout(10*time.Second),
			route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1<<20)),
		"http": route.HTTP("/a", route.Timeout(time.Minute), route.IdleTimeout(10*time.Second),
			route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1<<20)),
		"graphql": route.GraphQL("/g", nil, route.Timeout(time.Minute), route.IdleTimeout(10*time.Second),
			route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1<<20)),
		"wsproto": route.WSProto("/w", nil, route.Timeout(time.Minute), route.IdleTimeout(10*time.Second),
			route.Retry(3, time.Second, "5xx", "reset"), route.CORS(cors()), route.MaxRequestBytes(1<<20)),
	}
	for name, d := range decls {
		r, err := link.Routes.Decl(d)
		if err != nil || !proto.Equal(r.GetPolicy(), want) {
			t.Fatalf("%s declarative policy: %v %v", name, r.GetPolicy(), err)
		}
	}
}

func TestNoPolicyByDefault(t *testing.T) {
	t.Parallel()

	if p := link.Routes.HTTP([]route.HTTPOption{route.Listen(":1")}).Policy; p != nil {
		t.Fatalf("policy without policy options: %v", p)
	}

	r, _ := link.Routes.Decl(route.HTTP("/a"))
	if r.GetPolicy() != nil {
		t.Fatalf("declarative policy without options: %v", r.GetPolicy())
	}
}

func TestServerOptions(t *testing.T) {
	t.Parallel()

	var order []string

	tagged := func(name string) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			order = append(order, name)

			return next(ctx, req)
		}
	}
	streamA := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		return next(srv, ss)
	}
	streamB := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		return next(srv, ss)
	}

	g := link.Routes.GRPC([]route.GRPCOption{
		route.Interceptors(tagged("a"), tagged("b")), route.StreamInterceptors(streamA), route.Reflection(),
	})
	if len(g.Unary) != 2 || len(g.Stream) != 1 || !g.Reflection {
		t.Fatalf("grpc: %+v", g)
	}

	w := link.Routes.WS([]route.WSProtoOption{route.Interceptors(tagged("a")), route.StreamInterceptors(streamA, streamB)})
	if len(w.Unary) != 1 || len(w.Stream) != 2 {
		t.Fatalf("ws: %+v", w)
	}

	marking := func(tag string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, tag)

				next.ServeHTTP(w, r)
			})
		}
	}

	h := link.Routes.HTTP([]route.HTTPOption{route.Middleware(marking("outer")), route.Middleware(marking("inner"))})
	order = nil

	h.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if !slices.Equal(order, []string{"outer", "inner", "handler"}) {
		t.Fatalf("middleware order: %v", order)
	}

	if len(link.Routes.WS([]route.WSProtoOption{route.Middleware(marking("x"))}).Middleware) != 1 {
		t.Fatal("ws middleware")
	}
}
