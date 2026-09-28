package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane/internal/health"
	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

type group struct {
	ctx context.Context
}

func (g group) Go(_ string, fn func(context.Context) error) { go func() { _ = fn(g.ctx) }() }

func code(t *testing.T, h *health.Health, path string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	h.HTTP().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))

	return rec.Code
}

func grpcStatus(t *testing.T, h *health.Health) hv1.HealthCheckResponse_ServingStatus {
	t.Helper()

	res, err := h.GRPC().Check(context.Background(), &hv1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}

	return res.GetStatus()
}

func start(t *testing.T, h *health.Health) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var g lifecycle.Group = group{ctx}
	if err := h.Start(ctx, g); err != nil {
		t.Fatal(err)
	}
}

func TestReadyOnlyWhenServing(t *testing.T) {
	t.Parallel()

	h := health.New(testlog.Discard(), time.Hour)
	start(t, h)
	time.Sleep(20 * time.Millisecond)

	if c := code(t, h, "/healthz/readiness"); c == http.StatusOK {
		t.Fatalf("ready before serving: %d", c)
	}

	if c := code(t, h, "/healthz/liveness"); c != http.StatusOK {
		t.Fatalf("liveness: %d", c)
	}

	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c != http.StatusOK {
		t.Fatalf("ready after serving: %d", c)
	}

	if s := grpcStatus(t, h); s != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("grpc: %v", s)
	}

	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s := grpcStatus(t, h); s == hv1.HealthCheckResponse_SERVING {
		t.Fatal("still serving after stop")
	}
}

func TestAuthorProbeGatesReadiness(t *testing.T) {
	t.Parallel()

	h := health.New(testlog.Discard(), time.Hour)
	db := probe.NewBool()
	h.Add(health.Ready, db)
	start(t, h)
	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c == http.StatusOK {
		t.Fatal("ready with failing author probe")
	}

	db.Set(true)
	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c != http.StatusOK {
		t.Fatalf("not ready with passing probe: %d", c)
	}
}
