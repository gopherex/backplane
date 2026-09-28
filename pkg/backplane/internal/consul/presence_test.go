package consul_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/consul"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func client(t *testing.T) *api.Client {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

type group struct{ ctx context.Context }

func (g group) Go(_ string, fn func(context.Context) error) { go func() { _ = fn(g.ctx) }() }

func waitKey(t *testing.T, c *api.Client, key string, present bool) *api.KVPair {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		kv, _, err := c.KV().Get(key, nil)
		if err == nil && (kv != nil) == present {
			return kv
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("key %s present=%v not reached", key, present)

	return nil
}

//nolint:paralleltest // shares one Consul with other packages' tests
func TestPresenceLifecycle(t *testing.T) {
	c := client(t)
	id := consul.Identity{Service: "presence-test", Version: "1.2.3", Instance: "presence-test-1", Address: "127.0.0.1", PlatformPort: 1}
	manifest := &backplanev1.Manifest{Service: id.Service, Version: id.Version}

	t.Cleanup(func() { _, _ = c.KV().DeleteTree("backplane/services/presence-test/", nil) })

	p, err := consul.New(consul.Params{Client: c, Log: testlog.Discard(), Identity: id, Manifest: manifest, Register: true})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	raw := waitKey(t, c, "backplane/services/presence-test/manifests/1.2.3", true)

	var m backplanev1.Manifest
	if err := proto.Unmarshal(raw.Value, &m); err != nil || m.GetService() != "presence-test" {
		t.Fatalf("manifest: %v %v", &m, err)
	}

	state := waitKey(t, c, "backplane/services/presence-test/instances/presence-test-1", true)
	if state.Session == "" {
		t.Fatal("state not bound to a session")
	}

	services, _, err := c.Catalog().Service("presence-test", "", nil)
	if err != nil || len(services) != 1 {
		t.Fatalf("catalog: %v %v", services, err)
	}

	cancel()

	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	waitKey(t, c, "backplane/services/presence-test/instances/presence-test-1", false)

	services, _, _ = c.Catalog().Service("presence-test", "", nil)
	if len(services) != 0 {
		t.Fatalf("still in catalog: %v", services)
	}
}

// A crashed instance leaves its session; the restarted one with the same id
// must take over immediately.
//
//nolint:paralleltest // shares one Consul with other packages' tests
func TestRestartTakesOverStaleSession(t *testing.T) {
	c := client(t)
	id := consul.Identity{Service: "presence-restart", Version: "1.0.0", Instance: "presence-restart-1", Address: "127.0.0.1", PlatformPort: 1}
	key := "backplane/services/presence-restart/instances/presence-restart-1"

	t.Cleanup(func() { _, _ = c.KV().DeleteTree("backplane/services/presence-restart/", nil) })

	// An earlier incarnation created by this SDK: same name, short lock delay.
	stale, _, err := c.Session().Create(&api.SessionEntry{Name: id.Instance, TTL: "60s", Behavior: api.SessionBehaviorDelete, LockDelay: time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = c.Session().Destroy(stale, nil) })

	if ok, _, err := c.KV().Acquire(&api.KVPair{Key: key, Value: []byte("old"), Session: stale}, nil); err != nil || !ok {
		t.Fatalf("seed lock: %v %v", ok, err)
	}

	p, err := consul.New(consul.Params{Client: c, Log: testlog.Discard(), Identity: id, Manifest: &backplanev1.Manifest{Service: id.Service, Version: id.Version}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		kv, _, _ := c.KV().Get(key, nil)
		if kv != nil && kv.Session != stale && kv.Session != "" {
			cancel()

			_ = p.Stop(context.Background())

			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("stale session not taken over")
}
