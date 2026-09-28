// Package health is the single source of a service's health: probes are
// registered by kind, their composite status is cached in xprobe states, and
// grpc.health.v1, the HTTP probe endpoints and the Consul check all read the
// same states.
//
// Evaluation is serialized per kind: a periodic tick and a Serving flip
// never interleave, so a slow check started before the gate opened cannot
// overwrite the fresher result.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"
	"github.com/gopherex/xprobe/pkg/state"
	grpcprobe "github.com/gopherex/xprobe/pkg/transport/grpc"
	httpprobe "github.com/gopherex/xprobe/pkg/transport/http"

	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
)

// Kind of probe.
type Kind int

const (
	Live Kind = iota
	Ready
	Startup
)

const kinds = 3

func (k Kind) String() string { return [...]string{"liveness", "readiness", "startup"}[k] }

// Health owns probe states. The readiness state is the registry's "" entry,
// which grpc.health.v1 reports and Consul checks.
type Health struct {
	log      *xlog.Logger
	interval time.Duration
	reg      *state.Registry
	states   [kinds]*state.State
	serving  *probe.Bool

	mu       sync.Mutex
	probes   [kinds][]probe.Probe
	combined [kinds]probe.Probe
	eval     [kinds]sync.Mutex
}

// New creates the health component.
func New(log *xlog.Logger, interval time.Duration) *Health {
	reg := state.NewRegistry()
	health := &Health{log: log, interval: interval, reg: reg, serving: probe.NewBool()}
	health.states = [kinds]*state.State{state.New(), reg.Get(""), state.New()}

	for _, st := range health.states {
		st.Set(probe.StatusDown)
	}

	return health
}

// Add registers a probe of the given kind; call before Start.
func (h *Health) Add(k Kind, p probe.Probe) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.probes[k] = append(h.probes[k], p)
}

// Serving flips the SDK's own gate and re-evaluates readiness and startup
// immediately.
func (h *Health) Serving(ctx context.Context, on bool) {
	h.serving.Set(on)
	h.evaluate(ctx, Ready)
	h.evaluate(ctx, Startup)
}

// GRPC is the grpc.health.v1 server over the cached readiness.
func (h *Health) GRPC() hv1.HealthServer { return grpcprobe.New(h.reg) }

// HTTP serves the cached states at the xprobe default paths.
func (h *Health) HTTP() http.Handler {
	return httpprobe.Mux(
		httpprobe.NewHTTPProbe(httpprobe.DefaultLivenessPath, httpprobe.CachedHandler(h.states[Live])),
		httpprobe.NewHTTPProbe(httpprobe.DefaultReadinessPath, httpprobe.CachedHandler(h.states[Ready])),
		httpprobe.NewHTTPProbe(httpprobe.DefaultStartupPath, httpprobe.CachedHandler(h.states[Startup])),
	)
}

// Name implements lifecycle.Component.
func (h *Health) Name() string { return "health" }

// Start builds the composites and re-evaluates each kind every interval.
func (h *Health) Start(ctx context.Context, g lifecycle.Group) error {
	h.mu.Lock()
	h.combined[Live] = probe.All(h.probes[Live]...)
	h.combined[Ready] = probe.All(append([]probe.Probe{h.serving}, h.probes[Ready]...)...)
	h.combined[Startup] = probe.All(append([]probe.Probe{h.serving}, h.probes[Startup]...)...)
	h.mu.Unlock()

	for k := range Kind(kinds) {
		h.evaluate(ctx, k)
		g.Go(fmt.Sprintf("health.%s", k), func(ctx context.Context) error {
			h.tick(ctx, k)

			return nil
		})
	}

	return nil
}

// Stop reports not ready.
func (h *Health) Stop(ctx context.Context) error {
	h.Serving(ctx, false)

	return nil
}

func (h *Health) tick(ctx context.Context, k Kind) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.evaluate(ctx, k)
		}
	}
}

// evaluate checks kind k and stores the result; serialized per kind.
func (h *Health) evaluate(ctx context.Context, k Kind) {
	h.mu.Lock()
	composite := h.combined[k]
	h.mu.Unlock()

	if composite == nil {
		return
	}

	h.eval[k].Lock()
	defer h.eval[k].Unlock()

	status := composite.Check(ctx)
	if prev, changed := h.states[k].Set(status); changed {
		h.log.Info("health",
			xlog.String("probe", k.String()), xlog.String("from", prev.String()), xlog.String("to", status.String()))
	}
}
