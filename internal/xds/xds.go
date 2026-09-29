// Package xds is backplane's control plane for Envoy (§6): an ADS server
// (delta and state-of-the-world, envoyproxy/go-control-plane) whose one
// snapshot is rebuilt from the registry's Catalog on every change.
//
// Every Envoy gets the same snapshot whatever its node id or cluster (one
// node group): the listener, the route table, a cluster per service, port
// and protocol, and their endpoints — see Build.
//
// The ADS server listens on its own address (BACKPLANE_XDS_LISTEN), not on
// the SDK's public port: a public route would be announced in backplane's
// manifest and routed by Envoy itself.
package xds

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	clusterservice "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discoverygrpc "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	endpointservice "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	listenerservice "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	routeservice "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	serverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// ErrNoSnapshot: the first snapshot is not served yet.
var ErrNoSnapshot = errors.New("xds: no snapshot yet")

// group is the one node group every Envoy belongs to.
const group = "edge"

const (
	defaultDebounce = 200 * time.Millisecond
	// Envoy pings no more often than this; the server accepts it.
	keepaliveInterval = 15 * time.Second
)

// Config of the server.
type Config struct {
	// Listen is the ADS address (":18000").
	Listen string
	// Gateway is the listener and console the snapshot describes.
	Gateway Gateway
}

// Option configures New.
type Option func(*Server)

// Debounce is how long a change waits for more before the snapshot is
// rebuilt (default 200ms).
func Debounce(d time.Duration) Option { return func(s *Server) { s.debounce = d } }

// Server serves ADS to Envoy and follows the catalog. It is a component:
// the listener opens at start (a taken address fails the start) and closes
// at stop.
//
// A Server is shared by pointer: it holds the listener and the snapshot
// state.
type Server struct {
	deps.Component

	cfg      Config
	src      registry.Source
	cache    cachev3.SnapshotCache
	grpc     *grpc.Server
	debounce time.Duration
	metrics  metrics

	ready chan struct{}

	mu       sync.Mutex
	ln       net.Listener
	version  uint64
	hash     [sha256.Size]byte
	warnings []string
	nodes    map[int64]*corev3.Node // by stream
}

// New creates the server under parent, building snapshots from src.
func New(parent deps.Scope, cfg Config, src registry.Source, opts ...Option) *Server {
	s := &Server{
		Component: deps.NewComponent(parent, "xds"),
		cfg:       cfg,
		src:       src,
		debounce:  defaultDebounce,
		ready:     make(chan struct{}),
		nodes:     map[int64]*corev3.Node{},
	}
	for _, o := range opts {
		o(s)
	}

	s.metrics = newMetrics(s.Meter())
	s.cache = cachev3.NewSnapshotCache(true, oneGroup{}, logger{s.Log()})

	s.OnStart(s.listen)
	s.OnStop(s.close)
	s.Go(s.serve)
	s.Go(s.follow)

	return s
}

// Ready is nil once the first snapshot is served: until the registry has
// synced, Envoy keeps what it has rather than an empty configuration.
func (s *Server) Ready() error {
	select {
	case <-s.ready:
		return nil
	default:
		return ErrNoSnapshot
	}
}

// Addr is the ADS listener's address once started, nil before.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ln == nil {
		return nil
	}

	return s.ln.Addr()
}

// Version of the current snapshot; 0 before the first.
func (s *Server) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.version
}

func (s *Server) listen(ctx context.Context) error {
	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("xds listen %s: %w", s.cfg.Listen, err)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	s.grpc = grpc.NewServer(grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime: keepaliveInterval, PermitWithoutStream: true,
	}))

	// Streams end with the gRPC server, not with the start context.
	srv := serverv3.NewServer(context.Background(), s.cache, s.callbacks()) //nolint:contextcheck // see above
	discoverygrpc.RegisterAggregatedDiscoveryServiceServer(s.grpc, srv)
	listenerservice.RegisterListenerDiscoveryServiceServer(s.grpc, srv)
	routeservice.RegisterRouteDiscoveryServiceServer(s.grpc, srv)
	clusterservice.RegisterClusterDiscoveryServiceServer(s.grpc, srv)
	endpointservice.RegisterEndpointDiscoveryServiceServer(s.grpc, srv)

	s.Log().Info("xds listening", xlog.String("addr", ln.Addr().String()))

	return nil
}

// close tolerates a start that failed half way or a serve that never ran.
func (s *Server) close(context.Context) error {
	if s.grpc != nil {
		s.grpc.Stop()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ln != nil {
		_ = s.ln.Close() // closed already when the gRPC server served it
	}

	return nil
}

// serve runs the gRPC server until the node stops; ADS streams never end
// on their own, so the stop closes them at once.
func (s *Server) serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()

	done := make(chan error, 1)

	go func() { done <- s.grpc.Serve(ln) }()

	select {
	case err := <-done:
		return fmt.Errorf("xds serve: %w", err)
	case <-ctx.Done():
		s.grpc.Stop()
		<-done

		return nil
	}
}

// follow rebuilds the snapshot after catalog changes, debounced: a burst
// of changes makes one snapshot.
func (s *Server) follow(ctx context.Context) error {
	changes := s.src.Changes(ctx)

	for {
		s.apply(ctx, s.src.Current())

		if _, ok := <-changes; !ok {
			return nil
		}

		timer := time.NewTimer(s.debounce)

	wait:
		for {
			select {
			case _, ok := <-changes:
				if !ok {
					timer.Stop()

					return nil
				}
			case <-timer.C:
				break wait
			}
		}
	}
}

// apply builds and serves the snapshot of cat, unless it is the zero
// Catalog (the registry is not synced) or equals the served one.
func (s *Server) apply(ctx context.Context, cat registry.Catalog) {
	if cat.Index == 0 {
		return
	}

	res, err := Build(s.cfg.Gateway, cat)
	if err != nil {
		s.failed(ctx, "build", err)

		return
	}

	hash, err := digest(res)
	if err != nil {
		s.failed(ctx, "build", err)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.warn(res.Warnings)

	if s.version > 0 && hash == s.hash {
		return
	}

	version := s.version + 1

	snap, err := cachev3.NewSnapshot(strconv.FormatUint(version, 10), map[resource.Type][]types.Resource{
		resource.ListenerType: res.Listeners,
		resource.RouteType:    res.Routes,
		resource.ClusterType:  res.Clusters,
		resource.EndpointType: res.Endpoints,
	})
	if err == nil {
		err = snap.Consistent()
	}

	if err == nil {
		err = s.cache.SetSnapshot(ctx, group, snap)
	}

	if err != nil {
		s.failed(ctx, "snapshot", err)

		return
	}

	s.version, s.hash = version, hash
	s.metrics.snapshot(ctx, version, res)

	s.Log().Info("xds snapshot",
		xlog.Uint64("snapshot", version), xlog.Uint64("catalog", cat.Index),
		xlog.Int("services", res.ServiceCount), xlog.Int("routes", res.RouteCount),
		xlog.Int("clusters", len(res.Clusters)))

	if version == 1 {
		close(s.ready)
	}
}

// warn logs the build's warnings when they differ from the last build's.
func (s *Server) warn(ws []string) {
	if slices.Equal(ws, s.warnings) {
		return
	}

	s.warnings = ws
	for _, w := range ws {
		s.Log().Warn("xds: " + w)
	}
}

func (s *Server) failed(ctx context.Context, what string, err error) {
	s.metrics.error(ctx, what)
	s.Log().Error("xds snapshot not served", xlog.String("stage", what), xlog.Err(err))
}

// digest is the content hash of the resources: equal snapshots are not
// served again.
func digest(res Resources) ([sha256.Size]byte, error) {
	h := sha256.New()
	opts := proto.MarshalOptions{Deterministic: true}

	for _, list := range [][]types.Resource{res.Listeners, res.Routes, res.Clusters, res.Endpoints} {
		for _, r := range list {
			b, err := opts.Marshal(r)
			if err != nil {
				return [sha256.Size]byte{}, fmt.Errorf("xds: digest: %w", err)
			}

			_, _ = h.Write(b)
			_, _ = h.Write([]byte{0})
		}
	}

	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))

	return out, nil
}

// oneGroup puts every Envoy in one node group.
type oneGroup struct{}

func (oneGroup) ID(*corev3.Node) string { return group }
