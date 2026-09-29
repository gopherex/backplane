package xds_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	bufferv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/buffer/v3"
	corsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/cors/v3"
	transcoderv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_json_transcoder/v3"
	hcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	upstreamhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/xds"
)

const (
	helloSvc = "hello.v1.HelloService"
	pub      = 8080
	legacy   = 9001
)

func descriptors(t *testing.T) []byte {
	t.Helper()

	b, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{protodesc.ToFileDescriptorProto(hellov1.File_proto_hello_v1_hello_proto)},
	})
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func kind(k backplanev1.RouteKind, prefix string, port uint32) *backplanev1.Route {
	return &backplanev1.Route{Kind: k, Prefix: prefix, Port: port}
}

// helloManifest has a route of every kind, hosts, an own port and policies.
func helloManifest(t *testing.T) *backplanev1.Manifest {
	t.Helper()

	connect := kind(backplanev1.RouteKind_ROUTE_KIND_CONNECT, "/"+helloSvc+"/", pub)
	connect.Services = []string{helloSvc}
	connect.Policy = &backplanev1.RoutePolicy{
		Timeout:     durationpb.New(time.Minute),
		IdleTimeout: durationpb.New(30 * time.Second),
		Retry: &backplanev1.RetryPolicy{
			Attempts: 3, PerTryTimeout: durationpb.New(time.Second), RetryOn: []string{"unavailable", "reset"},
		},
	}

	ws := kind(backplanev1.RouteKind_ROUTE_KIND_WS_PROTO, "/ws/", pub)
	ws.Services = []string{helloSvc}

	http := kind(backplanev1.RouteKind_ROUTE_KIND_HTTP, "/hello/", pub)
	http.Policy = &backplanev1.RoutePolicy{
		Timeout: durationpb.New(5 * time.Second),
		Cors: &backplanev1.Cors{
			Origins: []string{"*"}, Methods: []string{"GET", "POST"}, Headers: []string{"X-A"},
			Credentials: true, MaxAge: durationpb.New(time.Hour),
		},
	}

	gql := kind(backplanev1.RouteKind_ROUTE_KIND_GRAPHQL, "/graphql/", pub)
	gql.Policy = &backplanev1.RoutePolicy{MaxRequestBytes: 64 << 10}

	api := kind(backplanev1.RouteKind_ROUTE_KIND_HTTP, "/api/", pub)
	api.Host = "api.example.com"

	wild := kind(backplanev1.RouteKind_ROUTE_KIND_HTTP, "/wild/", pub)
	wild.Host = "*.example.com"

	grpcOnly := kind(backplanev1.RouteKind_ROUTE_KIND_GRPC, "/other.v1.Other/", 0)
	grpcOnly.Services = []string{"other.v1.Other"}

	return &backplanev1.Manifest{
		Service: "hello", Version: "1.0.0", Descriptors: descriptors(t),
		Routes: []*backplanev1.Route{
			connect, ws, http, gql, api, wild, grpcOnly,
			kind(backplanev1.RouteKind_ROUTE_KIND_HTTP, "/legacy/", legacy),
		},
	}
}

func catalog(t *testing.T) registry.Catalog {
	t.Helper()

	state := &backplanev1.InstanceState{Version: "1.0.0"}

	return registry.Catalog{Index: 1, Services: map[string]registry.Service{
		"hello": {
			Name:      "hello",
			Manifests: map[string]*backplanev1.Manifest{"1.0.0": helloManifest(t)},
			Instances: []registry.Instance{
				{ID: "a", State: state, Registered: true, Healthy: true, Address: "10.0.0.1", Port: pub},
				{ID: "b", State: state, Registered: true, Healthy: false, Address: "10.0.0.2", Port: pub},
				{ID: "c", State: state, Registered: true, Healthy: true, Address: "hello-c.local", Port: pub},
				{ID: "d", State: state, Registered: true, Healthy: true, Address: "10.0.0.4"}, // no public port
				{ID: "e", State: state, Registered: false, Address: "10.0.0.5", Port: pub},
			},
		},
		"backplane": {
			Name:      "backplane",
			Instances: []registry.Instance{{ID: "bp", Registered: true, Healthy: true, Address: "10.0.0.9"}},
		},
		"empty": {Name: "empty"},
	}}
}

func build(t *testing.T, gw xds.Gateway, cat registry.Catalog) xds.Resources {
	t.Helper()

	res, err := xds.Build(gw, cat)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func named[T interface {
	types.Resource
	GetName() string
}](t *testing.T, list []types.Resource, name string) T {
	t.Helper()

	i := slices.IndexFunc(list, func(r types.Resource) bool {
		v, ok := r.(T)

		return ok && v.GetName() == name
	})
	if i < 0 {
		t.Fatalf("no resource %q", name)
	}

	return list[i].(T)
}

func assignment(t *testing.T, res xds.Resources, cluster string) []string {
	t.Helper()

	for _, r := range res.Endpoints {
		cla, ok := r.(*endpointv3.ClusterLoadAssignment)
		if !ok || cla.GetClusterName() != cluster {
			continue
		}

		var out []string

		for _, l := range cla.GetEndpoints() {
			for _, e := range l.GetLbEndpoints() {
				a := e.GetEndpoint().GetAddress().GetSocketAddress()
				out = append(out, a.GetAddress()+":"+strconv.FormatUint(uint64(a.GetPortValue()), 10))
			}
		}

		return out
	}

	t.Fatalf("no endpoints of %q", cluster)

	return nil
}

func vhost(t *testing.T, res xds.Resources, name string) *routev3.VirtualHost {
	t.Helper()

	rc := named[*routev3.RouteConfiguration](t, res.Routes, xds.RouteConfigName)
	for _, v := range rc.GetVirtualHosts() {
		if v.GetName() == name {
			return v
		}
	}

	t.Fatalf("no virtual host %q", name)

	return nil
}

func prefixes(v *routev3.VirtualHost) []string {
	out := make([]string, 0, len(v.GetRoutes()))
	for _, r := range v.GetRoutes() {
		m := r.GetMatch()
		out = append(out, m.GetPrefix()+m.GetPathSeparatedPrefix())
	}

	return out
}

func find(t *testing.T, v *routev3.VirtualHost, prefix string) *routev3.Route {
	t.Helper()

	for _, r := range v.GetRoutes() {
		if r.GetMatch().GetPrefix() == prefix || r.GetMatch().GetPathSeparatedPrefix() == prefix {
			return r
		}
	}

	t.Fatalf("%s: no route %s", v.GetName(), prefix)

	return nil
}

func TestClustersAndEndpoints(t *testing.T) {
	t.Parallel()

	res := build(t, xds.Gateway{Port: 10000}, catalog(t))

	names := make([]string, 0, len(res.Clusters))
	for _, r := range res.Clusters {
		if c, ok := r.(*clusterv3.Cluster); ok {
			names = append(names, c.GetName())
		}
	}

	want := []string{"hello_grpc", "hello_p8080_grpc", "hello_p8080_http", "hello_p9001_http"}
	if !slices.Equal(names, want) {
		t.Fatalf("clusters %v, want %v", names, want)
	}

	grpcCluster := named[*clusterv3.Cluster](t, res.Clusters, "hello_p8080_grpc")
	if grpcCluster.GetType() != clusterv3.Cluster_EDS || grpcCluster.GetEdsClusterConfig().GetEdsConfig().GetAds() == nil {
		t.Fatalf("not an ADS EDS cluster: %v", grpcCluster)
	}

	var opts upstreamhttpv3.HttpProtocolOptions
	if err := grpcCluster.GetTypedExtensionProtocolOptions()["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"].
		UnmarshalTo(&opts); err != nil || opts.GetExplicitHttpConfig().GetHttp2ProtocolOptions() == nil {
		t.Fatalf("grpc cluster is not h2: %v", err)
	}

	if http1 := named[*clusterv3.Cluster](t, res.Clusters, "hello_p8080_http"); len(http1.GetTypedExtensionProtocolOptions()) != 0 {
		t.Fatal("http cluster has protocol options")
	}

	// Healthy only; IP addresses only; the registered port, 0 left out.
	if got := assignment(t, res, "hello_grpc"); !slices.Equal(got, []string{"10.0.0.1:8080"}) {
		t.Errorf("registered port endpoints %v", got)
	}

	// An explicit port: every healthy instance, also one registered without a port.
	if got := assignment(t, res, "hello_p9001_http"); !slices.Equal(got, []string{"10.0.0.1:9001", "10.0.0.4:9001"}) {
		t.Errorf("port endpoints %v", got)
	}

	if res.ServiceCount != 1 || res.RouteCount != 8 {
		t.Errorf("counts: services %d routes %d", res.ServiceCount, res.RouteCount)
	}

	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "hello-c.local") }) {
		t.Errorf("no warning about the non-IP address: %v", res.Warnings)
	}
}

func TestRoutesAndPolicy(t *testing.T) {
	t.Parallel()

	res := build(t, xds.Gateway{Port: 10000}, catalog(t))
	def := vhost(t, res, xds.DefaultVHost)

	if !slices.Equal(def.GetDomains(), []string{"*"}) {
		t.Fatalf("default domains %v", def.GetDomains())
	}

	// Longest prefix first; host-bound routes stay in their hosts.
	want := []string{"/" + helloSvc + "/", "/other.v1.Other/", "/graphql/", "/legacy/", "/hello/", "/ws/"}
	if got := prefixes(def); !slices.Equal(got, want) {
		t.Fatalf("default routes %v, want %v", got, want)
	}

	connect := find(t, def, "/"+helloSvc+"/").GetRoute()
	if connect.GetCluster() != "hello_p8080_grpc" || connect.GetTimeout().AsDuration() != time.Minute ||
		connect.GetIdleTimeout().AsDuration() != 30*time.Second {
		t.Errorf("connect action %v", connect)
	}

	rp := connect.GetRetryPolicy()
	if rp.GetRetryOn() != "unavailable,reset" || rp.GetNumRetries().GetValue() != 2 || rp.GetPerTryTimeout().AsDuration() != time.Second {
		t.Errorf("retry %v", rp)
	}

	ws := find(t, def, "/ws/").GetRoute()
	if ws.GetCluster() != "hello_p8080_http" || len(ws.GetUpgradeConfigs()) != 1 ||
		ws.GetUpgradeConfigs()[0].GetUpgradeType() != "websocket" || !ws.GetUpgradeConfigs()[0].GetEnabled().GetValue() ||
		ws.GetTimeout() == nil || ws.GetTimeout().AsDuration() != 0 {
		t.Errorf("ws action %v", ws)
	}

	if other := find(t, def, "/other.v1.Other/").GetRoute(); other.GetCluster() != "hello_grpc" || other.GetTimeout() != nil {
		t.Errorf("grpc route %v", other)
	}

	if legacyRoute := find(t, def, "/legacy/").GetRoute(); legacyRoute.GetCluster() != "hello_p9001_http" {
		t.Errorf("legacy cluster %s", legacyRoute.GetCluster())
	}

	var cors corsv3.CorsPolicy
	if err := find(t, def, "/hello/").GetTypedPerFilterConfig()["envoy.filters.http.cors"].UnmarshalTo(&cors); err != nil {
		t.Fatal(err)
	}

	if cors.GetAllowOriginStringMatch()[0].GetExact() != "*" || cors.GetAllowMethods() != "GET,POST" ||
		cors.GetAllowHeaders() != "X-A" || !cors.GetAllowCredentials().GetValue() || cors.GetMaxAge() != "3600" {
		t.Errorf("cors %v", &cors)
	}

	var buf bufferv3.BufferPerRoute
	if err := find(t, def, "/graphql/").GetTypedPerFilterConfig()["envoy.filters.http.buffer"].UnmarshalTo(&buf); err != nil ||
		buf.GetBuffer().GetMaxRequestBytes().GetValue() != 64<<10 {
		t.Errorf("buffer %v %v", &buf, err)
	}
}

func TestConnectTranscoding(t *testing.T) {
	t.Parallel()

	res := build(t, xds.Gateway{Port: 10000}, catalog(t))
	def := vhost(t, res, xds.DefaultVHost)
	per := find(t, def, "/"+helloSvc+"/").GetTypedPerFilterConfig()

	if _, off := per["envoy.filters.http.grpc_web"]; off {
		t.Error("grpc_web disabled on a Connect route")
	}

	var tc transcoderv3.GrpcJsonTranscoder
	if err := per["envoy.filters.http.grpc_json_transcoder"].UnmarshalTo(&tc); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(tc.GetServices(), []string{helloSvc}) || !tc.GetAutoMapping() || len(tc.GetProtoDescriptorBin()) == 0 {
		t.Errorf("transcoder %v", tc.GetServices())
	}

	// A plain gRPC route gets neither.
	other := find(t, def, "/other.v1.Other/").GetTypedPerFilterConfig()

	var web routev3.FilterConfig
	if err := other["envoy.filters.http.grpc_web"].UnmarshalTo(&web); err != nil || !web.GetDisabled() || len(other) != 1 {
		t.Errorf("grpc route filters %v %v", other, err)
	}

	// Descriptors without the service: gRPC-Web only, and a warning.
	cat := catalog(t)
	m := proto.CloneOf(cat.Services["hello"].Manifests["1.0.0"])
	m.Routes[0].Services = []string{"missing.v1.Missing"}
	cat.Services["hello"].Manifests["1.0.0"] = m

	res = build(t, xds.Gateway{Port: 10000}, cat)
	per = find(t, vhost(t, res, xds.DefaultVHost), "/"+helloSvc+"/").GetTypedPerFilterConfig()

	if _, ok := per["envoy.filters.http.grpc_json_transcoder"]; ok || per["envoy.filters.http.grpc_web"] != nil {
		t.Errorf("bad descriptors: filters %v", per)
	}

	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "missing.v1.Missing") }) {
		t.Errorf("warnings %v", res.Warnings)
	}
}

func TestHosts(t *testing.T) {
	t.Parallel()

	res := build(t, xds.Gateway{Port: 10000}, catalog(t))

	api := vhost(t, res, "api.example.com")
	if !slices.Equal(api.GetDomains(), []string{"api.example.com"}) {
		t.Fatalf("domains %v", api.GetDomains())
	}

	// Its own route, the wildcard covering it, then the routes without a host.
	if got := prefixes(api); !slices.Contains(got, "/api/") || !slices.Contains(got, "/wild/") || !slices.Contains(got, "/hello/") {
		t.Errorf("api.example.com routes %v", got)
	}

	wild := vhost(t, res, "*.example.com")
	if got := prefixes(wild); slices.Contains(got, "/api/") || !slices.Contains(got, "/wild/") {
		t.Errorf("*.example.com routes %v", got)
	}

	if got := prefixes(vhost(t, res, xds.DefaultVHost)); slices.Contains(got, "/api/") || slices.Contains(got, "/wild/") {
		t.Errorf("default holds host routes: %v", got)
	}
}

func TestListener(t *testing.T) {
	t.Parallel()

	res := build(t, xds.Gateway{Port: 10000}, catalog(t))
	l := named[*listenerv3.Listener](t, res.Listeners, xds.ListenerName)

	if l.GetAddress().GetSocketAddress().GetPortValue() != 10000 {
		t.Fatalf("port %v", l.GetAddress())
	}

	var hcm hcmv3.HttpConnectionManager
	if err := l.GetFilterChains()[0].GetFilters()[0].GetTypedConfig().UnmarshalTo(&hcm); err != nil {
		t.Fatal(err)
	}

	if hcm.GetRds().GetRouteConfigName() != xds.RouteConfigName || hcm.GetRds().GetConfigSource().GetAds() == nil ||
		hcm.GetCodecType() != hcmv3.HttpConnectionManager_AUTO || !hcm.GetStripAnyHostPort() {
		t.Errorf("hcm %v", &hcm)
	}

	if u := hcm.GetUpgradeConfigs(); len(u) != 1 || u[0].GetUpgradeType() != "websocket" || u[0].GetEnabled().GetValue() {
		t.Errorf("websocket must be off by default, on per route: %v", u)
	}

	filters := make([]string, 0, len(hcm.GetHttpFilters()))

	for _, f := range hcm.GetHttpFilters() {
		name := f.GetName()
		if f.GetDisabled() {
			name += "(off)"
		}

		filters = append(filters, name)
	}

	want := []string{
		"envoy.filters.http.cors", "envoy.filters.http.grpc_web", "envoy.filters.http.grpc_json_transcoder(off)",
		"envoy.filters.http.buffer(off)", "envoy.filters.http.router",
	}
	if !slices.Equal(filters, want) {
		t.Errorf("filters %v", filters)
	}
}

func TestConsole(t *testing.T) {
	t.Parallel()

	console := xds.Console{Port: 8081, Service: "backplane", Fallback: "192.168.1.5"}

	cases := map[string]struct {
		host, prefix string
		vhost, match string
	}{
		"root":   {vhost: xds.DefaultVHost, match: "/"},
		"prefix": {prefix: "/backplane", vhost: xds.DefaultVHost, match: "/backplane"},
		"host":   {host: "console.example.com", vhost: "console.example.com", match: "/"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := console
			c.Host, c.Prefix = tc.host, tc.prefix
			res := build(t, xds.Gateway{Port: 10000, Console: c}, catalog(t))

			v := vhost(t, res, tc.vhost)
			r := find(t, v, tc.match).GetRoute()

			if r.GetCluster() != xds.ConsoleCluster || !r.GetUpgradeConfigs()[0].GetEnabled().GetValue() {
				t.Errorf("console route %v", r)
			}

			// The console's host serves the console only; the root console
			// comes after every service route.
			switch name {
			case "host":
				if len(v.GetRoutes()) != 1 {
					t.Errorf("console host routes %v", prefixes(v))
				}
			case "root":
				if last := prefixes(v)[len(v.GetRoutes())-1]; last != "/" {
					t.Errorf("console not last: %v", prefixes(v))
				}
			}

			// Healthy backplane instances at the console port.
			if got := assignment(t, res, xds.ConsoleCluster); !slices.Equal(got, []string{"10.0.0.9:8081"}) {
				t.Errorf("console endpoints %v", got)
			}
		})
	}

	// No healthy backplane in the catalog: this instance.
	cat := catalog(t)
	delete(cat.Services, "backplane")

	if got := assignment(t, build(t, xds.Gateway{Port: 10000, Console: console}, cat), xds.ConsoleCluster); !slices.Equal(got,
		[]string{"192.168.1.5:8081"}) {
		t.Errorf("fallback endpoints %v", got)
	}
}

func TestDuplicateRoutes(t *testing.T) {
	t.Parallel()

	cat := catalog(t)
	other := &backplanev1.Manifest{Service: "zz", Version: "1", Routes: []*backplanev1.Route{
		kind(backplanev1.RouteKind_ROUTE_KIND_HTTP, "/hello/", 0),
	}}
	cat.Services["zz"] = registry.Service{Name: "zz", Manifests: map[string]*backplanev1.Manifest{"1": other}}

	res := build(t, xds.Gateway{Port: 10000}, cat)

	r := find(t, vhost(t, res, xds.DefaultVHost), "/hello/")
	if r.GetRoute().GetCluster() != "hello_p8080_http" {
		t.Errorf("first service by name must win: %s", r.GetRoute().GetCluster())
	}

	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "shadowed") }) {
		t.Errorf("warnings %v", res.Warnings)
	}
}
