package xds_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"

	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/xds"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
)

// The Envoy of platform-in-a-box: bootstrap deployments/envoy/envoy.yaml,
// ADS at host.docker.internal:18000, the public listener on 10000.
const (
	envoyXDS    = ":18000"
	envoyPublic = "localhost:10000"
	envoyWait   = 60 * time.Second // Envoy's ADS reconnect backoff reaches 30s
)

// envoyEnv skips without the stack: BACKPLANE_TEST_ENVOY (Envoy admin,
// localhost:9901) and BACKPLANE_TEST_CONSUL.
func envoyEnv(t *testing.T) (string, *api.Client, string) {
	t.Helper()

	admin, addr := os.Getenv("BACKPLANE_TEST_ENVOY"), os.Getenv("BACKPLANE_TEST_CONSUL")
	if admin == "" || addr == "" {
		t.Skip("BACKPLANE_TEST_ENVOY and BACKPLANE_TEST_CONSUL not set (make up)")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	return admin, c, addr
}

// hostAddress is an address of this host that containers reach (Consul's
// health check, Envoy's upstream connections): the source address of the
// default route. Not host.docker.internal: that is the bridge gateway,
// which the service does not know as its own address.
func hostAddress(t *testing.T) string {
	t.Helper()

	conn, err := net.Dial("udp", "192.0.2.1:9") // TEST-NET-1: nothing is sent
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}

	return host
}

func freePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	return port
}

// runHello builds examples/hello and runs it registered in Consul at host.
func runHello(t *testing.T, c *api.Client, consul, host string) {
	t.Helper()

	// One hello per Consul: wait out another run's (conformance), refuse a
	// hello that stays.
	until(t, "no live hello instance on this Consul", func() (bool, string) {
		keys, _, err := c.KV().Keys("backplane/services/hello/instances/", "", nil)

		return err == nil && len(keys) == 0, fmt.Sprint(keys, err)
	})

	bin := filepath.Join(t.TempDir(), "hello")
	pkg := "github.com/gopherex/backplane/pkg/backplane/build"

	build := exec.Command("go", "build", "-o", bin, "-ldflags", "-X "+pkg+".Service=hello -X "+pkg+".Version=9.9.8",
		"../../examples/hello/cmd/hello")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hello: %v\n%s", err, out)
	}

	logs := &bytes.Buffer{}
	cmd := exec.Command(bin)

	cmd.Env = append(os.Environ(),
		"BACKPLANE_CONSUL_ADDR="+consul, "BACKPLANE_INSTANCE=xds-hello-1", "BACKPLANE_ADVERTISE="+host,
		"BACKPLANE_INTERNAL_PORT="+freePort(t), "BACKPLANE_PUBLIC_PORT="+freePort(t),
		"HELLO_LEGACY_LISTEN=:"+freePort(t),
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
	)
	cmd.Stdout, cmd.Stderr = logs, logs

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()

		_, _ = c.KV().DeleteTree("backplane/services/hello/", nil)

		if t.Failed() {
			t.Logf("hello logs:\n%s", logs)
		}
	})
}

func until(t *testing.T, what string, ok func() (bool, string)) {
	t.Helper()

	var last string

	deadline := time.Now().Add(envoyWait)
	for time.Now().Before(deadline) {
		var done bool
		if done, last = ok(); done {
			return
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("%s: not reached; last: %s", what, last)
}

func get(url string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err.Error()
	}

	return do(req)
}

func do(req *http.Request) (int, string) {
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body)
}

// TestEnvoy runs the control plane on Envoy's ADS port against the live
// catalog and drives hello's routes of every kind through Envoy.
//
//nolint:paralleltest // owns :18000 and the hello instance
func TestEnvoy(t *testing.T) {
	admin, c, consul := envoyEnv(t)
	host := hostAddress(t)
	before := rejected(t, admin)

	// A stand-in console on this host, placed under /backplane.
	console := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "console "+r.URL.Path)
		}),
		ReadHeaderTimeout: time.Second,
	}

	consoleLn, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = console.Serve(consoleLn) }()

	t.Cleanup(func() { _ = console.Close() })

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	reg := registry.New(h.Root(), c,
		registry.WaitTime(time.Second), registry.Backoff(10*time.Millisecond, 100*time.Millisecond))
	srv := xds.New(h.Root(), xds.Config{Listen: envoyXDS, Gateway: xds.Gateway{
		Port: 10000,
		Console: xds.Console{
			Prefix: "/backplane", Port: uint32(consoleLn.Addr().(*net.TCPAddr).Port),
			Service: "backplane-xds-test", Fallback: host,
		},
	}}, reg, xds.Debounce(50*time.Millisecond))
	h.Start()

	runHello(t, c, consul, host)

	until(t, "http through envoy", func() (bool, string) {
		code, body := get("http://" + envoyPublic + "/hello/?name=x")

		return code == http.StatusOK && strings.TrimSpace(body) == "Hello, x!", fmt.Sprintf("%d %s", code, body)
	})

	t.Run("grpc", func(t *testing.T) { grpcThroughEnvoy(t) })
	t.Run("grpc-web", func(t *testing.T) { grpcWebThroughEnvoy(t) })
	t.Run("json", func(t *testing.T) { jsonThroughEnvoy(t) })
	t.Run("ws-proto", func(t *testing.T) { wsThroughEnvoy(t) })
	t.Run("cors", func(t *testing.T) { corsThroughEnvoy(t) })

	t.Run("own port", func(t *testing.T) {
		if code, body := get("http://" + envoyPublic + "/legacy/"); code != http.StatusOK {
			t.Errorf("legacy: %d %q", code, body)
		}
	})

	t.Run("console", func(t *testing.T) {
		if code, body := get("http://" + envoyPublic + "/backplane/auth/login"); code != http.StatusOK ||
			body != "console /backplane/auth/login" {
			t.Errorf("console: %d %q", code, body)
		}
	})

	// Envoy accepted every version it was sent.
	if after := rejected(t, admin); after != before {
		t.Errorf("envoy rejected %d updates (see its log)", after-before)
	}

	t.Logf("snapshot version %d", srv.Version())
}

// rejected sums Envoy's xDS update_rejected counters (they outlive
// control-plane connections).
func rejected(t *testing.T, admin string) int {
	t.Helper()

	code, stats := get("http://" + admin + "/stats?filter=update_rejected")
	if code != http.StatusOK {
		t.Fatalf("envoy admin: %d %s", code, stats)
	}

	n := 0

	for line := range strings.Lines(stats) {
		if _, v, ok := strings.Cut(line, ": "); ok {
			k, _ := strconv.Atoi(strings.TrimSpace(v))
			n += k
		}
	}

	return n
}

func grpcThroughEnvoy(t *testing.T) {
	t.Helper()

	conn, err := grpc.NewClient(envoyPublic, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := hellov1.NewHelloServiceClient(conn)

	res, err := client.Greet(t.Context(), &hellov1.GreetRequest{Name: "grpc"})
	if err != nil || res.GetGreeting() != "Hello, grpc!" {
		t.Fatalf("greet: %v %v", res, err)
	}

	stream, err := client.Countdown(t.Context(), &hellov1.CountdownRequest{From: 2})
	if err != nil {
		t.Fatal(err)
	}

	n := 0
	for ; ; n++ {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}

	if n != 3 {
		t.Errorf("countdown: %d messages", n)
	}
}

// grpcWebThroughEnvoy speaks gRPC-Web over HTTP/1.1, as a browser does.
func grpcWebThroughEnvoy(t *testing.T) {
	t.Helper()

	msg, _ := proto.Marshal(&hellov1.GreetRequest{Name: "web"})
	frame := binary.BigEndian.AppendUint32([]byte{0}, uint32(len(msg)))
	frame = append(frame, msg...)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+envoyPublic+"/hello.v1.HelloService/Greet", bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")

	code, body := do(req)

	const header = 5
	if code != http.StatusOK || len(body) < header || body[0] != 0 {
		t.Fatalf("grpc-web: %d %q", code, body)
	}

	size := binary.BigEndian.Uint32([]byte(body[1:header]))

	var out hellov1.GreetResponse
	if err := proto.Unmarshal([]byte(body[header:header+int(size)]), &out); err != nil || out.GetGreeting() != "Hello, web!" {
		t.Errorf("grpc-web response: %v %v", out.GetGreeting(), err)
	}
}

// jsonThroughEnvoy: REST-JSON transcoding of the Connect route.
func jsonThroughEnvoy(t *testing.T) {
	t.Helper()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+envoyPublic+"/hello.v1.HelloService/Greet", strings.NewReader(`{"name":"json"}`))
	req.Header.Set("Content-Type", "application/json")

	code, body := do(req)

	var out struct {
		Greeting string `json:"greeting"`
	}

	if err := json.Unmarshal([]byte(body), &out); code != http.StatusOK || err != nil || out.Greeting != "Hello, json!" {
		t.Errorf("json: %d %q %v", code, body, err)
	}
}

func wsThroughEnvoy(t *testing.T) {
	t.Helper()

	cc, err := wsrpc.Dial(t.Context(), "ws://"+envoyPublic+"/ws/")
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	ws, err := cc.NewStream(t.Context(), "/hello.v1.HelloService/Greet", nil)
	if err != nil {
		t.Fatal(err)
	}

	_ = ws.Send(&hellov1.GreetRequest{Name: "ws"})
	_ = ws.CloseSend()

	var out hellov1.GreetResponse
	if err := ws.Recv(&out); err != nil || out.GetGreeting() != "Hello, ws!" {
		t.Errorf("ws-proto: %q %v", out.GetGreeting(), err)
	}
}

// corsThroughEnvoy: hello's /hello/ allows any origin for GET.
func corsThroughEnvoy(t *testing.T) {
	t.Helper()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodOptions, "http://"+envoyPublic+"/hello/", http.NoBody)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" && got != "*" {
		t.Errorf("preflight: %d allow-origin %q", res.StatusCode, got)
	}
}
