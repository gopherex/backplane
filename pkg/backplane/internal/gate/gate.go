// Package gate admits internal API calls only while the author's tree is
// up: before it started they get Unavailable, and stopping waits for the
// calls in flight before the tree (and its dependencies) stops. Health is
// never gated.
package gate

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const healthPrefix = "/grpc.health.v1.Health/"

// Gate counts admitted calls.
type Gate struct {
	mu       sync.Mutex
	open     bool
	inflight int
	idle     chan struct{} // closed when inflight drops to 0 after Close
}

// New creates a closed gate.
func New() *Gate { return &Gate{} }

// Open admits calls.
func (g *Gate) Open() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.open = true
}

// Close stops admitting and waits for admitted calls within ctx.
func (g *Gate) Close(ctx context.Context) error {
	g.mu.Lock()
	g.open = false

	if g.inflight == 0 {
		g.mu.Unlock()

		return nil
	}

	// Concurrent closes share one channel: replacing it would strand the
	// first waiter until its ctx ends.
	if g.idle == nil {
		g.idle = make(chan struct{})
	}

	idle := g.idle
	g.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("gate: internal calls still running: %w", ctx.Err())
	}
}

func (g *Gate) enter(method string) bool {
	if strings.HasPrefix(method, healthPrefix) {
		return true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.open {
		return false
	}

	g.inflight++

	return true
}

func (g *Gate) leave(method string) {
	if strings.HasPrefix(method, healthPrefix) {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.inflight--
	if g.inflight == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

func unavailable() error {
	return status.Error(codes.Unavailable, "service not serving internal API") //nolint:wrapcheck // gRPC status
}

// ServerOptions installs the gate on a gRPC server.
func (g *Gate) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(
			ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
		) (any, error) {
			if !g.enter(info.FullMethod) {
				return nil, unavailable()
			}
			defer g.leave(info.FullMethod)

			return next(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(
			srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler,
		) error {
			if !g.enter(info.FullMethod) {
				return unavailable()
			}
			defer g.leave(info.FullMethod)

			return next(srv, stream)
		}),
	}
}
