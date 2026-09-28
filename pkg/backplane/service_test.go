package backplane_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/guard"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

var errDown = errors.New("down")

type testConfig struct {
	config.Backplane `json:"backplane"`

	Greeting config.Live[string] `json:"greeting" schemapb:"default=hi"`
}

type testState struct {
	backplane.Root[testConfig]

	Conn deps.Dependency[string]
}

func freePort(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	return port
}

func get(url string, header ...string) (int, string) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}

	t.Fatalf("%s: timed out", what)
}

// TestServiceEndToEnd: a required dependency holds readiness until it comes
// up; public route, guarded UI, gRPC health, Consul state and a clean stop.
func TestServiceEndToEnd(t *testing.T) {
	platformPort, publicPort := freePort(t), freePort(t)
	consul := os.Getenv("BACKPLANE_TEST_CONSUL")

	t.Setenv("BACKPLANE_INTERNAL_PORT", platformPort)
	t.Setenv("BACKPLANE_PUBLIC_PORT", publicPort)
	t.Setenv("BACKPLANE_INTERNAL_SECRET", "s3cret")
	t.Setenv("BACKPLANE_CONSUL_ADDR", consul)

	var connUp atomic.Bool

	conn := deps.Func(func(context.Context) (string, error) {
		if !connUp.Load() {
			return "", errDown
		}

		return "conn", nil
	}, deps.WithName[string]("conn"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc, err := backplane.Open(ctx, func(root backplane.Root[testConfig]) (*testState, error) {
		return &testState{Root: root, Conn: deps.NewDependency(root, conn, deps.Retry(10*time.Millisecond, 20*time.Millisecond))}, nil
	}, backplane.Name("e2e"), backplane.Version("1.0.0"), backplane.Instance("e2e-1"),
		backplane.Advertise("127.0.0.1"), backplane.Logger(testlog.Discard()),
		backplane.WithConfig(config.WithoutFile()))
	if err != nil {
		t.Fatal(err)
	}

	svc.HTTP("/hi/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, svc.State().Config().Greeting.Get()+" "+svc.State().Conn.Get())
	}))
	svc.UI(fstest.MapFS{"plugin.json": {Data: []byte(`{"sdk_major":1}`)}})

	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	platform := "http://127.0.0.1:" + platformPort

	time.Sleep(100 * time.Millisecond)

	if code, _ := get(platform + "/healthz/readiness"); code == http.StatusOK {
		t.Fatal("ready before the required dependency")
	}

	connUp.Store(true)
	eventually(t, "ready", func() bool { code, _ := get(platform + "/healthz/readiness"); return code == http.StatusOK })

	if code, body := get("http://127.0.0.1:" + publicPort + "/hi/"); code != http.StatusOK || body != "hi conn" {
		t.Fatalf("public route: %d %q", code, body)
	}

	if code, _ := get(platform + "/_backplane/ui/plugin.json"); code != http.StatusForbidden {
		t.Fatalf("ui without secret: %d", code)
	}

	if code, _ := get(platform+"/_backplane/ui/plugin.json", guard.HTTPHeader, "s3cret"); code != http.StatusOK {
		t.Fatalf("ui with secret: %d", code)
	}

	cc, err := grpc.NewClient("127.0.0.1:"+platformPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	if res, err := hv1.NewHealthClient(cc).Check(ctx, &hv1.HealthCheckRequest{}); err != nil ||
		res.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("grpc health: %v %v", res, err)
	}

	if consul != "" {
		assertRegistered(t, consul)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	if code, _ := get(platform + "/healthz/readiness"); code != 0 {
		t.Fatalf("platform port still open: %d", code)
	}
}

func TestOpenFailsOnConstructorError(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")

	_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return nil, errDown
	}, backplane.Name("e2e-fail"), backplane.Logger(testlog.Discard()), backplane.WithConfig(config.WithoutFile()))
	if !errors.Is(err, errDown) {
		t.Fatalf("err = %v", err)
	}
}

func assertRegistered(t *testing.T, addr string) {
	t.Helper()

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = c.KV().DeleteTree("backplane/services/e2e/", nil) })
	eventually(t, "instance state", func() bool {
		kv, _, _ := c.KV().Get("backplane/services/e2e/instances/e2e-1", nil)

		return kv != nil
	})
}
