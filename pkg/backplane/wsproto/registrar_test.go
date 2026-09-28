package wsproto //nolint:testpackage // exercises the unexported registrar directly

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/ws-proto/wsrpc"
)

func TestRegistrarUnaryAndServerStream(t *testing.T) {
	t.Parallel()

	srv := wsrpc.NewServer(wsrpc.WithInsecureSkipOriginCheck())
	reg := newRegistrar(srv)
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", hv1.HealthCheckResponse_SERVING)
	hv1.RegisterHealthServer(reg, healthSrv)

	if len(reg.services) != 1 || reg.services[0] != "grpc.health.v1.Health" {
		t.Fatalf("services: %v", reg.services)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx := context.Background()

	cc, err := wsrpc.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	unary, err := cc.NewStream(ctx, "/grpc.health.v1.Health/Check", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := unary.Send(&hv1.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}

	_ = unary.CloseSend()

	var res hv1.HealthCheckResponse
	if err := unary.Recv(&res); err != nil || res.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("check: %v %v", res.GetStatus(), err)
	}

	watch, err := cc.NewStream(ctx, "/grpc.health.v1.Health/Watch", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := watch.Send(&hv1.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}

	_ = watch.CloseSend()

	var first hv1.HealthCheckResponse
	if err := watch.Recv(&first); err != nil || first.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("watch: %v %v", first.GetStatus(), err)
	}

	healthSrv.SetServingStatus("", hv1.HealthCheckResponse_NOT_SERVING)

	var second hv1.HealthCheckResponse
	if err := watch.Recv(&second); err != nil || second.GetStatus() != hv1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("watch update: %v %v", second.GetStatus(), err)
	}
}
