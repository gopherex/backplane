package routes

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc"
)

// Dispatch runs a registration's own interceptors on a gRPC server shared
// by several registrations: one public port is one grpc.Server, so author
// interceptors are keyed by the service a call's full method names.
type Dispatch struct {
	mu     sync.RWMutex
	unary  map[string]grpc.UnaryServerInterceptor
	stream map[string]grpc.StreamServerInterceptor
}

// NewDispatch creates an empty dispatch.
func NewDispatch() *Dispatch {
	return &Dispatch{
		unary:  map[string]grpc.UnaryServerInterceptor{},
		stream: map[string]grpc.StreamServerInterceptor{},
	}
}

// Add installs the interceptors for the named services.
func (d *Dispatch) Add(services []string, unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor) {
	d.mu.Lock()
	defer d.mu.Unlock()

	u, s := ChainUnary(unary), ChainStream(stream)
	for _, name := range services {
		if u != nil {
			d.unary[name] = u
		}

		if s != nil {
			d.stream[name] = s
		}
	}
}

// ServerOptions installs the dispatch on a server.
func (d *Dispatch) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(d.Unary()),
		grpc.ChainStreamInterceptor(d.Stream()),
	}
}

// Unary runs the unary interceptors of the called service.
func (d *Dispatch) Unary() grpc.UnaryServerInterceptor { return d.interceptUnary }

// Stream runs the stream interceptors of the called service.
func (d *Dispatch) Stream() grpc.StreamServerInterceptor { return d.interceptStream }

func (d *Dispatch) interceptUnary(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
) (any, error) {
	d.mu.RLock()
	icpt := d.unary[ServiceOf(info.FullMethod)]
	d.mu.RUnlock()

	if icpt == nil {
		return next(ctx, req)
	}

	return icpt(ctx, req, info, next)
}

func (d *Dispatch) interceptStream(
	srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler,
) error {
	d.mu.RLock()
	icpt := d.stream[ServiceOf(info.FullMethod)]
	d.mu.RUnlock()

	if icpt == nil {
		return next(srv, stream)
	}

	return icpt(srv, stream, info, next)
}

// ServiceOf is the service of a full method ("/pkg.Svc/Method" -> "pkg.Svc").
func ServiceOf(fullMethod string) string {
	s := strings.TrimPrefix(fullMethod, "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[:i]
	}

	return s
}

// ChainUnary folds interceptors into one, the first outermost; nil when
// there are none.
func ChainUnary(list []grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	switch len(list) {
	case 0:
		return nil
	case 1:
		return list[0]
	}

	head, tail := list[0], ChainUnary(list[1:])

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		return head(ctx, req, info, func(ctx context.Context, req any) (any, error) {
			return tail(ctx, req, info, next)
		})
	}
}

// ChainStream folds interceptors into one, the first outermost; nil when
// there are none.
func ChainStream(list []grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	switch len(list) {
	case 0:
		return nil
	case 1:
		return list[0]
	}

	head, tail := list[0], ChainStream(list[1:])

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		return head(srv, ss, info, func(srv any, ss grpc.ServerStream) error {
			return tail(srv, ss, info, next)
		})
	}
}
