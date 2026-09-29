package console_test

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
)

// Real HTTP cookie, ws-proto and bundle-proxy acceptance. Only the registry and
// session persistence are in memory; the browser uses the production TS client.
func TestBrowserConsole(t *testing.T) {
	t.Parallel()

	if os.Getenv("BACKPLANE_TEST_BROWSER") != "1" {
		t.Skip("build web fixtures, then set BACKPLANE_TEST_BROWSER=1")
	}

	web, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}

	shell := fstest.MapFS{}

	err = fs.WalkDir(os.DirFS(filepath.Join(web, "apps", "embedding", "dist")), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		body, err := os.ReadFile(filepath.Join(web, "apps", "embedding", "dist", path))
		if err != nil {
			return err
		}

		if path == "index.html" {
			body = []byte(strings.Replace(string(body), "<head>", `<head><meta name="backplane-fixture" content="live">`, 1))
		}

		shell[path] = &fstest.MapFile{Data: body, Mode: 0o644}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var bundleRequests atomic.Int64

	files := http.StripPrefix("/_backplane/ui/", http.FileServer(http.Dir(filepath.Join(web, "templates", "module", "dist", "plugin"))))
	module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(console.SecretHTTPHeader) != "relay-secret" {
			http.Error(w, "internal secret required", http.StatusForbidden)
			return
		}

		bundleRequests.Add(1)
		w.Header().Set("ETag", `"fixture-v1"`)
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(module.Close)
	_, portText, _ := net.SplitHostPort(module.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	hub := registry.NewHub()
	m := manifest("hello", "1.0.0")
	m.Ui = &backplanev1.UI{Hash: "fixture-v1", SdkMajor: 0}
	hub.Publish(map[string]registry.Service{
		"hello":             {Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": m}, Instances: []registry.Instance{instance("hello-1", "1.0.0", "127.0.0.1", port, serving, true)}},
		"browser-live-data": {Name: "browser-live-data"},
	})
	h := backplanetest.New(t, backplanetest.Name("backplane"))
	c := console.New(h.Root(), console.Settings{Prefix: "/backplane", AdminToken: adminToken, InternalSecret: "relay-secret", InsecureCookie: true}, newMemSessions(), hub, console.WithShell(shell))
	h.Start()

	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "tests/console-browser.mjs", srv.URL, adminToken)
	cmd.Dir = web

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}

	if bundleRequests.Load() == 0 {
		t.Fatal("browser never used the module bundle proxy")
	}

	t.Log(string(output))
}

func TestTelemetryWithoutOperatorSession(t *testing.T) {
	t.Parallel()
	h := backplanetest.New(t, backplanetest.Name("backplane"))
	admission := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			t.Errorf("unexpected admission path %q", r.URL.Path)
		}

		w.WriteHeader(http.StatusAccepted)
	})
	c := console.New(h.Root(), console.Settings{Prefix: "/backplane", AdminToken: adminToken}, newMemSessions(), registry.NewHub(), console.WithTelemetry(admission))
	h.Start()

	request := httptest.NewRequest(http.MethodPost, "/backplane/telemetry/v1/logs", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("public admission was gated by the operator session: %d", response.Code)
	}
}
