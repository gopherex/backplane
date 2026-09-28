// Package wsproto serves gRPC services over ws-proto (protobuf RPC over
// WebSocket) as a managed public route. It takes the same register function
// as Service.GRPC, so one implementation serves both.
package wsproto

import (
	"maps"
	"net/http"
	"slices"

	"google.golang.org/grpc"

	"github.com/gopherex/ws-proto/wsrpc"
	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

// Host is where the endpoint is mounted: any backplane.Service.
type Host interface {
	HTTP(prefix string, h http.Handler, opts ...route.HTTPOption)
	Log() *xlog.Logger
}

// Serve mounts a ws-proto endpoint at path on a public port and announces it
// with descriptors derived from the registered services.
func Serve(
	svc Host, path string, origins route.Origins,
	register func(grpc.ServiceRegistrar), opts ...route.HTTPOption,
) {
	srv := wsrpc.NewServer(originOption(origins))
	reg := wsrpc.GRPCRegistrar(srv)
	register(reg)

	var services []string
	if si, ok := reg.(serviceInfo); ok {
		services = slices.Sorted(maps.Keys(si.GetServiceInfo()))
	}

	fds, err := manifest.Descriptors(services)
	if err != nil {
		svc.Log().Warn("ws-proto descriptors", xlog.String("path", path), xlog.Err(err))
	}

	svc.HTTP(path, srv, append(opts, route.AsWSProto(fds))...)
}

// serviceInfo is implemented by wsrpc.GRPCRegistrar, like *grpc.Server.
type serviceInfo interface {
	GetServiceInfo() map[string]grpc.ServiceInfo
}

// originOption maps the policy onto wsrpc: its own check allows same-origin
// and the listed patterns, fails closed otherwise.
func originOption(o route.Origins) wsrpc.ServerOption {
	if o.IsAny() {
		return wsrpc.WithInsecureSkipOriginCheck()
	}

	return wsrpc.WithOriginPatterns(o.Patterns()...)
}
