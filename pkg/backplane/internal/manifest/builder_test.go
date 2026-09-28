package manifest_test

import (
	"errors"
	"testing"
	"testing/fstest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
)

func TestServicesDiff(t *testing.T) {
	t.Parallel()

	srv := grpc.NewServer()
	first := manifest.Services(srv, func(r grpc.ServiceRegistrar) { hv1.RegisterHealthServer(r, health.NewServer()) })

	second := manifest.Services(srv, func(grpc.ServiceRegistrar) {})
	if len(first) != 1 || first[0] != "grpc.health.v1.Health" || len(second) != 0 {
		t.Fatalf("first %v second %v", first, second)
	}
}

func TestGRPCRoutesCarryDescriptors(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.GRPC([]string{"grpc.health.v1.Health"}, backplanev1.RouteKind_ROUTE_KIND_GRPC, 8080, "")

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	if len(m.GetRoutes()) != 1 {
		t.Fatalf("routes: %v", m.GetRoutes())
	}

	r := m.GetRoutes()[0]
	if r.GetPrefix() != "/grpc.health.v1.Health/" || r.GetPort() != 8080 {
		t.Fatalf("route: %v", r)
	}

	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(r.GetDescriptors(), &fds); err != nil || len(fds.GetFile()) == 0 {
		t.Fatalf("descriptors: %v %d", err, len(fds.GetFile()))
	}
}

func TestUI(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.UI(fstest.MapFS{
		"plugin.json":    {Data: []byte(`{"sdk_major": 2}`)},
		"remoteEntry.js": {Data: []byte("x")},
	})

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	if m.GetUi().GetSdkMajor() != 2 || len(m.GetUi().GetHash()) != 64 {
		t.Fatalf("ui: %v", m.GetUi())
	}
}

func TestFirstErrorKept(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	first := errors.New("first")
	b.Fail(first)
	b.GRPC([]string{"does.not.Exist"}, backplanev1.RouteKind_ROUTE_KIND_GRPC, 1, "")

	if _, err := b.Build(); !errors.Is(err, first) {
		t.Fatalf("want first, got %v", err)
	}
}

func TestUnknownServiceFails(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.GRPC([]string{"does.not.Exist"}, backplanev1.RouteKind_ROUTE_KIND_GRPC, 1, "")

	if _, err := b.Build(); err == nil {
		t.Fatal("expected error")
	}
}
