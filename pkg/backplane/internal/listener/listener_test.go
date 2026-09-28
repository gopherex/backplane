package listener_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/backplane/pkg/backplane/internal/listener"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

type group struct{ ctx context.Context }

func (g group) Go(_ string, fn func(context.Context) error) { go func() { _ = fn(g.ctx) }() }

func TestGRPCAndHTTPOnOnePort(t *testing.T) {
	t.Parallel()

	srv := grpc.NewServer()
	hv1.RegisterHealthServer(srv, health.NewServer())

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })

	l := listener.New("test", "127.0.0.1:0", srv, mux, testlog.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := l.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get("http://" + l.Addr() + "/ping")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if string(body) != "pong" {
		t.Fatalf("http: %q", body)
	}

	conn, err := grpc.NewClient(l.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	out, err := hv1.NewHealthClient(conn).Check(ctx, &hv1.HealthCheckRequest{})
	if err != nil || out.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("grpc: %v %v", out, err)
	}

	if err := l.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if _, err := http.Get("http://" + l.Addr() + "/ping"); err == nil {
		t.Fatal("still serving after stop")
	}
}
