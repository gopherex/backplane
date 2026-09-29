package xds_test

import (
	"context"
	"slices"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/xds"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
)

const waitFor = 5 * time.Second

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("%s: not reached", what)
}

// deltaClusters opens a delta ADS stream as an Envoy and returns the
// cluster names of the first response.
func deltaClusters(t *testing.T, addr, node string) []string {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()

	stream, err := discoveryv3.NewAggregatedDiscoveryServiceClient(conn).DeltaAggregatedResources(ctx)
	if err != nil {
		t.Fatal(err)
	}

	err = stream.Send(&discoveryv3.DeltaDiscoveryRequest{
		Node: &corev3.Node{Id: node, Cluster: "test"}, TypeUrl: resource.ClusterType,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(res.GetResources()))
	for _, r := range res.GetResources() {
		names = append(names, r.GetName())
	}

	slices.Sort(names)

	return names
}

func TestServer(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	hub := registry.NewHub()
	srv := xds.New(h.Root(), xds.Config{Listen: "127.0.0.1:0", Gateway: xds.Gateway{Port: 10000}}, hub,
		xds.Debounce(10*time.Millisecond))
	h.Start()

	// Not synced: nothing served, so Envoy keeps what it has.
	time.Sleep(50 * time.Millisecond)

	if srv.Ready() == nil || srv.Version() != 0 {
		t.Fatalf("served before the registry synced: %d", srv.Version())
	}

	cat := catalog(t)
	hub.Publish(cat.Services)
	eventually(t, "first snapshot", func() bool { return srv.Ready() == nil })

	if srv.Version() != 1 {
		t.Fatalf("version %d", srv.Version())
	}

	// Every node id gets the same snapshot.
	want := []string{"hello_grpc", "hello_p8080_grpc", "hello_p8080_http", "hello_p9001_http"}
	for _, node := range []string{"edge", "another-envoy"} {
		if got := deltaClusters(t, srv.Addr().String(), node); !slices.Equal(got, want) {
			t.Fatalf("%s: clusters %v, want %v", node, got, want)
		}
	}

	// A catalog change that changes no resource serves nothing new.
	services := catalog(t).Services
	services["hello"].Instances[0].Tags = []string{"canary"}

	hub.Publish(services)
	time.Sleep(100 * time.Millisecond)

	if srv.Version() != 1 {
		t.Fatalf("unchanged resources bumped the version: %d", srv.Version())
	}

	// A new healthy instance does.
	services = catalog(t).Services
	services["hello"].Instances[1].Healthy = true

	hub.Publish(services)
	eventually(t, "second snapshot", func() bool { return srv.Version() == 2 })
}
