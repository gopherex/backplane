// Package temporaltest runs the SDK's Temporal tests against a dev server
// (BACKPLANE_TEST_TEMPORAL, e.g. localhost:7233) and plays backplane: a
// Nexus endpoint named after the service routed to a test worker that
// serves <service>.Hooks.
package temporaltest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
)

const (
	// Namespace of the dev server.
	Namespace = "default"
	nameBytes = 5
)

// Addr is the dev server's address; the test is skipped without one.
func Addr(tb testing.TB) string {
	tb.Helper()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		tb.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	return addr
}

// Quiet is a Temporal logger that writes nowhere.
func Quiet() tlog.Logger { return tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)) }

// Dial connects to the dev server; the client closes at cleanup.
func Dial(tb testing.TB) client.Client {
	tb.Helper()

	c, err := client.DialContext(tb.Context(), client.Options{HostPort: Addr(tb), Namespace: Namespace, Logger: Quiet()})
	if err != nil {
		tb.Fatalf("temporal: %v", err)
	}

	tb.Cleanup(c.Close)

	return c
}

// Name is a unique service name: [a-z][a-z0-9-]*.
func Name(prefix string) string {
	b := make([]byte, nameBytes)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}

// Serve runs a worker on queue with what register adds; it stops at
// cleanup.
func Serve(tb testing.TB, c client.Client, queue string, register func(w worker.Worker)) {
	tb.Helper()

	w := worker.New(c, queue, worker.Options{})
	register(w)

	if err := w.Start(); err != nil {
		tb.Fatalf("worker %s: %v", queue, err)
	}

	tb.Cleanup(w.Stop)
}

// Hooks plays backplane for service: the Nexus endpoint <service> routed to
// a test queue whose worker serves <service>.Hooks with ops. The endpoint
// is deleted at cleanup.
func Hooks(tb testing.TB, c client.Client, service string, ops ...nexus.RegisterableOperation) {
	tb.Helper()

	queue := "bp-" + service
	svc := nexus.NewService(service + ".Hooks")

	if err := svc.Register(ops...); err != nil {
		tb.Fatal(err)
	}

	Serve(tb, c, queue, func(w worker.Worker) { w.RegisterNexusService(svc) })
	Endpoint(tb, c, service, queue)
}

// Endpoint creates the Nexus endpoint name targeting queue; it is deleted
// at cleanup.
func Endpoint(tb testing.TB, c client.Client, name, queue string) {
	tb.Helper()

	res, err := c.OperatorService().CreateNexusEndpoint(tb.Context(), &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: name,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: Namespace, TaskQueue: queue},
			}},
		},
	})
	if err != nil {
		tb.Fatalf("nexus endpoint %s: %v", name, err)
	}

	tb.Cleanup(func() {
		_, _ = c.OperatorService().DeleteNexusEndpoint(context.Background(), &operatorservice.DeleteNexusEndpointRequest{
			Id: res.GetEndpoint().GetId(), Version: res.GetEndpoint().GetVersion(),
		})
	})
}

// Group runs goroutines until cleanup, then cancels and waits for them. A
// goroutine error fails the test.
type Group struct {
	tb     testing.TB
	ctx    context.Context //nolint:containedctx // the group's lifetime
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGroup creates a group stopped at cleanup.
func NewGroup(tb testing.TB) *Group {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	g := &Group{tb: tb, ctx: ctx, cancel: cancel}

	tb.Cleanup(g.Stop)

	return g
}

// Go implements node.Group.
func (g *Group) Go(fn func(ctx context.Context) error) {
	g.wg.Add(1)

	go func() {
		defer g.wg.Done()

		if err := fn(g.ctx); err != nil && g.ctx.Err() == nil {
			g.tb.Errorf("goroutine: %v", err)
		}
	}()
}

// Stop cancels the goroutines and waits for them.
func (g *Group) Stop() {
	g.cancel()
	g.wg.Wait()
}
