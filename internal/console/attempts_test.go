package console_test

import (
	"os"
	"testing"
	"time"

	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/infra/valkey"
)

// TestValkeyAttempts runs the limits against BACKPLANE_TEST_VALKEY
// (platform-in-a-box: localhost:6379) in database 15; skipped without.
func TestValkeyAttempts(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY not set")
	}

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	client := deps.NewDependency(h.Root(), valkey.New(valkey.Config{Addr: addr, DB: 15}))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	c := client.Get()
	if err := c.Do(t.Context(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}

	a := console.NewValkey(client, console.LoginLimits{
		Burst: 2, Window: 500 * time.Millisecond,
		Threshold: 3, Base: 200 * time.Millisecond, Max: 300 * time.Millisecond, Forget: 10 * time.Second,
	})
	allow := func(addr string) time.Duration {
		t.Helper()

		wait, err := a.Allow(t.Context(), addr)
		if err != nil {
			t.Fatal(err)
		}

		return wait
	}

	// Per address: Burst attempts per window, then the rest of the window.
	for i := range 2 {
		if wait := allow("198.51.100.1"); wait != 0 {
			t.Fatalf("attempt %d refused for %v", i, wait)
		}
	}

	if wait := allow("198.51.100.1"); wait <= 0 || wait > 500*time.Millisecond {
		t.Fatalf("over the burst: wait %v", wait)
	}

	if wait := allow("198.51.100.2"); wait != 0 {
		t.Fatalf("another address refused for %v", wait)
	}

	time.Sleep(600 * time.Millisecond)

	if wait := allow("198.51.100.1"); wait != 0 {
		t.Fatalf("next window refused for %v", wait)
	}

	// Global: from the threshold on everyone waits, doubling up to Max.
	for range 3 {
		if err := a.Failed(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if wait := allow("192.0.2.1"); wait <= 0 || wait > 200*time.Millisecond {
		t.Fatalf("backoff: wait %v", wait)
	}

	if err := a.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	if wait := allow("192.0.2.2"); wait <= 200*time.Millisecond || wait > 300*time.Millisecond {
		t.Fatalf("capped backoff: wait %v", wait)
	}

	time.Sleep(350 * time.Millisecond)

	if wait := allow("192.0.2.3"); wait != 0 {
		t.Fatalf("after the backoff: wait %v", wait)
	}

	// A success resets the failures: the next one is below the threshold.
	if err := a.Succeeded(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := a.Failed(t.Context()); err != nil {
		t.Fatal(err)
	}

	if wait := allow("192.0.2.4"); wait != 0 {
		t.Fatalf("after a success: wait %v", wait)
	}
}
