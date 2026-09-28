package config_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xconf"

	"github.com/gopherex/backplane/pkg/backplane/config"
)

type Postgres struct {
	DSN      config.Secret       `json:"dsn"`
	LogLevel config.Live[string] `json:"log_level" schemapb:"default=warn"`
	Timeout  time.Duration       `json:"timeout"   schemapb:"default=5s"`
}

type Config struct {
	config.Backplane `json:"backplane"`

	Postgres Postgres            `json:"postgres"`
	Salute   string              `json:"salute"   schemapb:"default=Hello"`
	Suffix   config.Live[string] `json:"suffix"   schemapb:"default=!"`
}

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hello.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadLayers(t *testing.T) {
	path := write(t, "postgres:\n  dsn: postgres://x\n  log_level: info\nsalute: Hi\n")
	t.Setenv("HELLO_SUFFIX", "?")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "consul:8500")

	cfg, err := config.Load[Config](context.Background(), config.Service("hello"), config.File(path))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Postgres.DSN.Reveal() != "postgres://x" || cfg.Postgres.LogLevel.Get() != "info" || cfg.Salute != "Hi" {
		t.Fatalf("file layer: %+v", cfg)
	}

	if cfg.Suffix.Get() != "?" || cfg.Postgres.Timeout != 5*time.Second {
		t.Fatalf("env/defaults: %q %v", cfg.Suffix.Get(), cfg.Postgres.Timeout)
	}

	if cfg.BackplaneConfig().Consul.Addr != "consul:8500" || cfg.BackplaneConfig().InternalPort != 9400 {
		t.Fatalf("block: %+v", cfg.BackplaneConfig())
	}
}

func TestSecretNeverPrints(t *testing.T) {
	t.Parallel()

	s := config.Secret("hunter2")
	for _, out := range []string{s.String(), fmt.Sprintf("%v %s %q %#v", s, s, s, s)} {
		if out != "***" && out != `*** *** "***" ***` {
			t.Fatalf("leaked: %s", out)
		}
	}
}

func TestLivePathsFromSchema(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")

	rt, err := config.Open[Config](context.Background(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	got := fmt.Sprint(rt.Live())
	if got != "[/postgres/log_level /suffix]" {
		t.Fatalf("live paths: %s", got)
	}

	if rt.Value().Suffix.Get() != "!" {
		t.Fatalf("value: %q", rt.Value().Suffix.Get())
	}
}

func TestOpenWithoutReachableConsul(t *testing.T) {
	t.Setenv("HELLO_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	rt, err := config.Open[Config](context.Background(), config.Service("hello"), config.WithoutFile())
	if err != nil {
		t.Fatalf("must open without consul: %v", err)
	}
	defer rt.Close()

	if rt.Degraded() == nil {
		t.Fatal("expected degraded")
	}
}

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func TestLiveReloadFromConsul(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := config.Consul{Addr: addr}.Client()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = client.KV().DeleteTree("config/live-test/", nil) })
	t.Setenv("LIVE_TEST_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	rt, err := config.Open[Config](context.Background(), config.Service("live-test"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	section := &rt.Value().Postgres // a component holding its section sees live changes
	applied := make(chan string, 4)

	section.LogLevel.Watch(func(v string) { applied <- v })

	put := func(key, value string) {
		if _, err := client.KV().Put(&api.KVPair{Key: "config/live-test/" + key, Value: []byte(value)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("salute", "ignored") // static: KV must not apply
	put("postgres/log_level", "debug")

	select {
	case v := <-applied:
		if v != "debug" {
			t.Fatalf("applied %q", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no live update")
	}

	if rt.Value().Salute != "Hello" {
		t.Fatalf("static field changed from KV: %q", rt.Value().Salute)
	}

	if src := rt.Effective().Sources["postgres.log_level"].String(); src != "CONFIG_SOURCE_KV" {
		t.Fatalf("source: %s", src)
	}
}

type GatedConfig struct {
	config.Backplane `json:"backplane"`

	Postgres Postgres            `json:"postgres"`
	Gate     config.Live[string] `json:"gate"` // required, only Consul may hold it
}

func TestOpenWaitsForRequiredLiveWhileConsulDown(t *testing.T) {
	t.Setenv("GATED_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()

	_, err := config.Open[GatedConfig](ctx,
		config.Service("gated"), config.WithoutFile(), config.RetryBackoff(10*time.Millisecond, 50*time.Millisecond))

	var degraded *xconf.DegradedError
	if !errors.As(err, &degraded) {
		t.Fatalf("want DegradedError, got %v", err)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}

	if since := time.Since(start); since < 250*time.Millisecond || since > 3*time.Second {
		t.Fatalf("waited %v", since)
	}
}

func TestOpenFailsFastOnMissingStaticWhileConsulDown(t *testing.T) {
	t.Setenv("BACKPLANE_CONSUL_ADDR", "127.0.0.1:1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()

	if _, err := config.Open[GatedConfig](ctx, config.Service("gated"), config.WithoutFile()); err == nil {
		t.Fatal("missing postgres.dsn must fail")
	}

	if since := time.Since(start); since > time.Second {
		t.Fatalf("static error must not wait for Consul: %v", since)
	}
}

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func TestOpenRequiredLiveFromConsul(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := config.Consul{Addr: addr}.Client()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = client.KV().DeleteTree("config/gated-test/", nil) })

	if _, err := client.KV().Put(&api.KVPair{Key: "config/gated-test/gate", Value: []byte("open")}, nil); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GATED_TEST_POSTGRES_DSN", "x")
	t.Setenv("BACKPLANE_CONSUL_ADDR", addr)

	rt, err := config.Open[GatedConfig](context.Background(), config.Service("gated-test"), config.WithoutFile())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if got := rt.Value().Gate.Get(); got != "open" {
		t.Fatalf("gate: %q", got)
	}
}
