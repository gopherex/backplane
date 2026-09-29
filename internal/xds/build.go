package xds

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	bufferv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/buffer/v3"
	corsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/cors/v3"
	transcoderv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_json_transcoder/v3"
	grpcwebv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_web/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	upstreamhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/registry"
)

// Names of what the snapshot holds besides the services' clusters.
const (
	// ListenerName is Envoy's public HTTP listener; RouteConfigName is its
	// route table (RDS).
	ListenerName    = "public"
	RouteConfigName = "public"
	// DefaultVHost is the virtual host of routes without a host ("*").
	DefaultVHost = "default"
	// ConsoleCluster leads to the console listeners of backplane.
	ConsoleCluster = "backplane_console"
)

// HTTP filters of the listener, in chain order. cors and router run for
// every route, grpc_web for every route but those that disable it (all
// but Connect); transcoder and buffer are disabled on the listener and
// enabled by the routes that configure them (typed_per_filter_config).
const (
	filterCORS       = "envoy.filters.http.cors"
	filterGRPCWeb    = "envoy.filters.http.grpc_web"
	filterTranscoder = "envoy.filters.http.grpc_json_transcoder"
	filterBuffer     = "envoy.filters.http.buffer"
	filterRouter     = "envoy.filters.http.router"
	filterHCM        = "envoy.filters.network.http_connection_manager"

	upstreamHTTPOptions = "envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
	websocket           = "websocket"
)

// Protocols of a service's clusters: the public port multiplexes gRPC
// (HTTP/2, matched on the first request's content-type) and HTTP/1.1, so
// a connection must carry one kind only.
const (
	protoGRPC = "grpc" // h2: gRPC and Connect routes (gRPC-Web and JSON are transcoded to gRPC)
	protoHTTP = "http" // HTTP/1.1: HTTP, GraphQL and ws-proto (a websocket upgrade)
)

const (
	connectTimeout = 2 * time.Second
	// placeholderBufferBytes configures the listener-level buffer filter,
	// which never runs: every route overrides it or leaves it disabled.
	placeholderBufferBytes = 1 << 20
	// defaultRetryOn: failures where the request never reached the service.
	defaultRetryOn = "connect-failure,refused-stream"
)

// Gateway is what the snapshot describes besides the catalog: Envoy's
// public listener and the console of the installation.
type Gateway struct {
	// Port of Envoy's public HTTP listener.
	Port uint32
	// Console routes; a zero Port leaves them out.
	Console Console
}

// Console places backplane's console behind Envoy (§11.3): on its own host,
// under a prefix of the default virtual host, or — neither — at "/" of the
// default virtual host, after every service route.
type Console struct {
	Host   string
	Prefix string
	// Port of the console listener on every backplane instance.
	Port uint32
	// Service is the catalog name of backplane: its healthy instances serve
	// the console at Address:Port.
	Service string
	// Fallback is the address used while the catalog lists no healthy
	// backplane instance (this instance's advertise address); empty: none.
	Fallback string
}

// Resources is one snapshot's content with what the build noticed.
type Resources struct {
	Listeners []types.Resource
	Routes    []types.Resource
	Clusters  []types.Resource
	Endpoints []types.Resource

	// Services with at least one route; Routes: service routes placed.
	ServiceCount int
	RouteCount   int
	// Warnings: what was left out and why (bad address, duplicate route,
	// descriptors without the route's service, ...).
	Warnings []string
}

// clusterKey is one upstream cluster of a service: a port (0 = the port
// the instance registered in Consul) and a protocol.
type clusterKey struct {
	service string
	port    uint32
	wire    string
}

// ClusterName of a service's cluster: "<service>_<proto>" at the
// registered port, "<service>_p<port>_<proto>" at an explicit route port.
// "_" never occurs in a service name, so the service is the part before
// the first "_" (envoy_cluster_name=~"<service>_.*").
func ClusterName(service string, port uint32, wire string) string {
	if port == 0 {
		return service + "_" + wire
	}

	return service + "_p" + strconv.FormatUint(uint64(port), 10) + "_" + wire
}

func (k clusterKey) name() string { return ClusterName(k.service, k.port, k.wire) }

// entry is a route placed in the table, before the virtual hosts are laid out.
type entry struct {
	service string
	host    string // lower-case; "" = any host
	match   string // prefix (or the console's separated prefix)
	route   *routev3.Route
}

type builder struct {
	gw       Gateway
	warnings []string
	err      error
}

// Build turns a catalog into Envoy resources: one listener, one route
// table, a cluster per service, port and protocol, their endpoints. Every
// service contributes the routes of its Latest manifest; traffic goes to
// its Healthy instances.
func Build(gateway Gateway, cat registry.Catalog) (Resources, error) {
	b := &builder{gw: gateway}

	var (
		entries []entry
		keys    = map[clusterKey]bool{}
		out     Resources
	)

	names := make([]string, 0, len(cat.Services))
	for name := range cat.Services {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		es := b.service(cat.Services[name], keys)
		if len(es) > 0 {
			out.ServiceCount++
		}

		entries = append(entries, es...)
	}

	out.RouteCount = len(entries)

	for _, k := range sortedKeys(keys) {
		out.Clusters = append(out.Clusters, b.cluster(k.name(), k.wire))
		out.Endpoints = append(out.Endpoints, b.endpoints(k.name(), b.addresses(cat.Services[k.service], k.port)))
	}

	if gateway.Console.Port != 0 {
		entries = append(entries, b.console())
		out.Clusters = append(out.Clusters, b.cluster(ConsoleCluster, protoHTTP))
		out.Endpoints = append(out.Endpoints, b.endpoints(ConsoleCluster, b.consoleAddresses(cat)))
	}

	out.Routes = []types.Resource{&routev3.RouteConfiguration{Name: RouteConfigName, VirtualHosts: b.vhosts(entries)}}
	out.Listeners = []types.Resource{b.listener()}
	out.Warnings = b.warnings

	return out, b.err
}

func (b *builder) warnf(format string, args ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, args...))
}

// any packs m; a failure (never expected for these messages) fails Build.
func (b *builder) any(m proto.Message) *anypb.Any {
	a, err := anypb.New(m)
	if err != nil && b.err == nil {
		b.err = fmt.Errorf("xds: pack %T: %w", m, err)
	}

	return a
}

// service places the routes of svc's latest manifest and records the
// clusters they lead to.
func (b *builder) service(svc registry.Service, keys map[clusterKey]bool) []entry {
	m := svc.Latest()
	if m == nil {
		return nil
	}

	out := make([]entry, 0, len(m.GetRoutes()))
	seen := map[string]bool{}

	for _, r := range m.GetRoutes() {
		wire := protocol(r.GetKind())
		if wire == "" {
			b.warnf("%s: route %s of kind %v: skipped", svc.Name, r.GetPrefix(), r.GetKind())

			continue
		}

		if !strings.HasPrefix(r.GetPrefix(), "/") {
			b.warnf("%s: route prefix %q does not start with /: skipped", svc.Name, r.GetPrefix())

			continue
		}

		host := strings.ToLower(r.GetHost())
		if seen[host+r.GetPrefix()] {
			b.warnf("%s: route %s%s declared twice: the first one kept", svc.Name, host, r.GetPrefix())

			continue
		}

		seen[host+r.GetPrefix()] = true

		key := clusterKey{service: svc.Name, port: r.GetPort(), wire: wire}
		keys[key] = true

		out = append(out, entry{
			service: svc.Name, host: host, match: r.GetPrefix(),
			route: b.route(svc.Name, m, r, key.name()),
		})
	}

	return out
}

func protocol(k backplanev1.RouteKind) string {
	switch k {
	case backplanev1.RouteKind_ROUTE_KIND_GRPC, backplanev1.RouteKind_ROUTE_KIND_CONNECT:
		return protoGRPC
	case backplanev1.RouteKind_ROUTE_KIND_HTTP, backplanev1.RouteKind_ROUTE_KIND_GRAPHQL,
		backplanev1.RouteKind_ROUTE_KIND_WS_PROTO:
		return protoHTTP
	default:
		return ""
	}
}

func sortedKeys(keys map[clusterKey]bool) []clusterKey {
	out := make([]clusterKey, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}

	slices.SortFunc(out, func(a, c clusterKey) int { return strings.Compare(a.name(), c.name()) })

	return out
}

// addresses of svc's healthy instances at port (0: each instance's
// registered port; instances registered without one are left out).
func (b *builder) addresses(svc registry.Service, port uint32) []*corev3.SocketAddress {
	var out []*corev3.SocketAddress

	for _, in := range svc.Healthy() {
		p := cmp.Or(port, in.Port)
		if p == 0 {
			continue
		}

		if net.ParseIP(in.Address) == nil {
			b.warnf("%s: instance %s address %q is not an IP: left out", svc.Name, in.ID, in.Address)

			continue
		}

		out = append(out, socket(in.Address, p))
	}

	return dedupe(out)
}

func (b *builder) consoleAddresses(cat registry.Catalog) []*corev3.SocketAddress {
	c := b.gw.Console

	var out []*corev3.SocketAddress

	for _, in := range cat.Services[c.Service].Healthy() {
		if net.ParseIP(in.Address) != nil {
			out = append(out, socket(in.Address, c.Port))
		}
	}

	if len(out) == 0 && net.ParseIP(c.Fallback) != nil {
		out = append(out, socket(c.Fallback, c.Port))
	}

	return dedupe(out)
}

func dedupe(addrs []*corev3.SocketAddress) []*corev3.SocketAddress {
	slices.SortFunc(addrs, func(a, c *corev3.SocketAddress) int {
		return cmp.Or(strings.Compare(a.GetAddress(), c.GetAddress()), cmp.Compare(a.GetPortValue(), c.GetPortValue()))
	})

	return slices.CompactFunc(addrs, func(a, c *corev3.SocketAddress) bool {
		return a.GetAddress() == c.GetAddress() && a.GetPortValue() == c.GetPortValue()
	})
}

func socket(addr string, port uint32) *corev3.SocketAddress {
	return &corev3.SocketAddress{Address: addr, PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: port}}
}

func ads() *corev3.ConfigSource {
	return &corev3.ConfigSource{
		ResourceApiVersion:    corev3.ApiVersion_V3,
		ConfigSourceSpecifier: &corev3.ConfigSource_Ads{Ads: &corev3.AggregatedConfigSource{}},
	}
}

// cluster is an EDS cluster; gRPC clusters speak HTTP/2 upstream,
// HTTP clusters HTTP/1.1.
func (b *builder) cluster(name, wire string) *clusterv3.Cluster {
	c := &clusterv3.Cluster{
		Name:                 name,
		ConnectTimeout:       durationpb.New(connectTimeout),
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_EDS},
		EdsClusterConfig:     &clusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()},
		LbPolicy:             clusterv3.Cluster_ROUND_ROBIN,
	}

	if wire == protoGRPC {
		c.TypedExtensionProtocolOptions = map[string]*anypb.Any{
			upstreamHTTPOptions: b.any(&upstreamhttpv3.HttpProtocolOptions{
				UpstreamProtocolOptions: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_{
					ExplicitHttpConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig{
						ProtocolConfig: &upstreamhttpv3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
							Http2ProtocolOptions: &corev3.Http2ProtocolOptions{},
						},
					},
				},
			}),
		}
	}

	return c
}

func (b *builder) endpoints(cluster string, addrs []*corev3.SocketAddress) *endpointv3.ClusterLoadAssignment {
	lbs := make([]*endpointv3.LbEndpoint, 0, len(addrs))
	for _, a := range addrs {
		lbs = append(lbs, &endpointv3.LbEndpoint{
			HostIdentifier: &endpointv3.LbEndpoint_Endpoint{Endpoint: &endpointv3.Endpoint{
				Address: &corev3.Address{Address: &corev3.Address_SocketAddress{SocketAddress: a}},
			}},
		})
	}

	return &endpointv3.ClusterLoadAssignment{
		ClusterName: cluster,
		Endpoints:   []*endpointv3.LocalityLbEndpoints{{LbEndpoints: lbs}},
	}
}

// console is the console's route: "/" of its host, its prefix
// (path-separated: "/backplane" and "/backplane/..."), or "/" of the
// default virtual host. The console gets the full path and upgrades /ws.
func (b *builder) console() entry {
	c := b.gw.Console
	match := &routev3.RouteMatch{PathSpecifier: &routev3.RouteMatch_Prefix{Prefix: "/"}}
	e := entry{service: c.Service, host: strings.ToLower(c.Host), match: "/"}

	if c.Prefix != "" {
		match.PathSpecifier = &routev3.RouteMatch_PathSeparatedPrefix{PathSeparatedPrefix: c.Prefix}
		e.match = c.Prefix
	}

	e.route = &routev3.Route{
		Name:                 "console",
		Match:                match,
		TypedPerFilterConfig: map[string]*anypb.Any{filterGRPCWeb: b.disabled()},
		Action: &routev3.Route_Route{Route: &routev3.RouteAction{
			ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: ConsoleCluster},
			Timeout:          durationpb.New(0),
			UpgradeConfigs:   []*routev3.RouteAction_UpgradeConfig{{UpgradeType: websocket, Enabled: wrapperspb.Bool(true)}},
		}},
	}

	return e
}

// vhosts lays the entries out: a virtual host per distinct host plus the
// default one. Like the SDK's Go mux, the longest prefix wins, and for
// one prefix the best host: exact, then the longest wildcard, then none —
// so every host's virtual host also holds the routes of the wildcards
// that cover it and the routes without a host.
func (b *builder) vhosts(entries []entry) []*routev3.VirtualHost {
	hosts := map[string]bool{}

	for _, e := range entries {
		if e.host != "" {
			hosts[e.host] = true
		}
	}

	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}

	slices.Sort(names)

	out := make([]*routev3.VirtualHost, 0, len(names)+1)
	for _, h := range names {
		out = append(out, &routev3.VirtualHost{Name: h, Domains: []string{h}, Routes: b.table(h, entries)})
	}

	return append(out, &routev3.VirtualHost{Name: DefaultVHost, Domains: []string{"*"}, Routes: b.table("", entries)})
}

// table is the ordered route list of the virtual host of domain ("" = *).
func (b *builder) table(domain string, entries []entry) []*routev3.Route {
	type ranked struct {
		entry

		rank int // host specificity: exact > longer wildcard > none
	}

	var placed []ranked

	// The console's own host is the console's alone.
	exclusive := b.gw.Console.Port != 0 && domain != "" && strings.EqualFold(domain, b.gw.Console.Host)

	for _, e := range entries {
		if exclusive && e.host != domain {
			continue
		}

		if rank, ok := covers(e.host, domain); ok {
			placed = append(placed, ranked{entry: e, rank: rank})
		}
	}

	slices.SortStableFunc(placed, func(a, c ranked) int {
		return cmp.Or(cmp.Compare(len(c.match), len(a.match)), strings.Compare(a.match, c.match), cmp.Compare(c.rank, a.rank))
	})

	out := make([]*routev3.Route, 0, len(placed))

	for i, r := range placed {
		if i > 0 && placed[i-1].match == r.match && placed[i-1].rank == r.rank {
			b.warnf("%s: route %s%s is shadowed by %s's", r.service, r.host, r.match, placed[i-1].service)

			continue
		}

		out = append(out, r.route)
	}

	return out
}

// covers reports whether a route of host serves requests to domain, and
// how specifically.
func covers(host, domain string) (int, bool) {
	switch {
	case host == "":
		return 0, true
	case host == domain:
		return 1 << 16, true //nolint:mnd // above any wildcard length
	case strings.HasPrefix(host, "*.") && domain != "" && strings.HasSuffix(domain, host[1:]):
		return len(host), true
	default:
		return 0, false
	}
}

// listener is the public HTTP listener: HTTP/1.1 and h2c, routes by RDS,
// websocket upgrades per route, the filters routes enable.
func (b *builder) listener() *listenerv3.Listener {
	disabled := func(name string, cfg proto.Message) *hcmv3.HttpFilter {
		return &hcmv3.HttpFilter{
			Name: name, Disabled: true, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: b.any(cfg)},
		}
	}

	manager := &hcmv3.HttpConnectionManager{
		CodecType:  hcmv3.HttpConnectionManager_AUTO,
		StatPrefix: ListenerName,
		RouteSpecifier: &hcmv3.HttpConnectionManager_Rds{Rds: &hcmv3.Rds{
			RouteConfigName: RouteConfigName, ConfigSource: ads(),
		}},
		UseRemoteAddress: wrapperspb.Bool(true),
		StripPortMode:    &hcmv3.HttpConnectionManager_StripAnyHostPort{StripAnyHostPort: true},
		// The port Host loses: the console's same-origin check needs it.
		AppendXForwardedPort: true,
		UpgradeConfigs: []*hcmv3.HttpConnectionManager_UpgradeConfig{
			{UpgradeType: websocket, Enabled: wrapperspb.Bool(false)},
		},
		HttpFilters: []*hcmv3.HttpFilter{
			{Name: filterCORS, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: b.any(&corsv3.Cors{})}},
			{Name: filterGRPCWeb, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: b.any(&grpcwebv3.GrpcWeb{})}},
			disabled(filterTranscoder, &transcoderv3.GrpcJsonTranscoder{
				DescriptorSet: &transcoderv3.GrpcJsonTranscoder_ProtoDescriptorBin{},
			}),
			disabled(filterBuffer, &bufferv3.Buffer{MaxRequestBytes: wrapperspb.UInt32(placeholderBufferBytes)}),
			{Name: filterRouter, ConfigType: &hcmv3.HttpFilter_TypedConfig{TypedConfig: b.any(&routerv3.Router{})}},
		},
	}

	return &listenerv3.Listener{
		Name: ListenerName,
		Address: &corev3.Address{Address: &corev3.Address_SocketAddress{
			SocketAddress: socket("0.0.0.0", b.gw.Port),
		}},
		FilterChains: []*listenerv3.FilterChain{{Filters: []*listenerv3.Filter{{
			Name: filterHCM, ConfigType: &listenerv3.Filter_TypedConfig{TypedConfig: b.any(manager)},
		}}}},
	}
}
