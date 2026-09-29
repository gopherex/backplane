package xds

import (
	"math"
	"strconv"
	"strings"

	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	bufferv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/buffer/v3"
	corsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/cors/v3"
	transcoderv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_json_transcoder/v3"
	matcherv3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// route is one manifest route as an Envoy route: prefix match, the
// cluster, the route's policy, and the filters its kind enables.
func (b *builder) route(service string, m *backplanev1.Manifest, r *backplanev1.Route, cluster string) *routev3.Route {
	kind := r.GetKind()
	action := &routev3.RouteAction{ClusterSpecifier: &routev3.RouteAction_Cluster{Cluster: cluster}}
	perFilter := map[string]*anypb.Any{}

	b.policy(service, r, action, perFilter)

	switch kind { //nolint:exhaustive // the other kinds need nothing beyond the policy
	case backplanev1.RouteKind_ROUTE_KIND_WS_PROTO:
		action.UpgradeConfigs = []*routev3.RouteAction_UpgradeConfig{
			{UpgradeType: websocket, Enabled: wrapperspb.Bool(true)},
		}
	case backplanev1.RouteKind_ROUTE_KIND_CONNECT:
		if t := b.transcoder(service, m, r); t != nil {
			perFilter[filterTranscoder] = b.any(t)
		}
	}

	// gRPC-Web is Connect's alone. Envoy rejects an empty per-route config
	// that would enable a filter disabled on the listener, and grpc_web has
	// no config of its own: it runs on the listener and is disabled here.
	if kind != backplanev1.RouteKind_ROUTE_KIND_CONNECT {
		perFilter[filterGRPCWeb] = b.disabled()
	}

	out := &routev3.Route{
		Name:   service + ":" + r.GetHost() + r.GetPrefix(),
		Match:  &routev3.RouteMatch{PathSpecifier: &routev3.RouteMatch_Prefix{Prefix: r.GetPrefix()}},
		Action: &routev3.Route_Route{Route: action},
	}

	if len(perFilter) > 0 {
		out.TypedPerFilterConfig = perFilter
	}

	return out
}

func (b *builder) disabled() *anypb.Any { return b.any(&routev3.FilterConfig{Disabled: true}) }

// policy applies Route.policy; unset fields keep the platform defaults:
// Envoy's (a 15s route timeout, no retries, no CORS, no body limit), except
// that a ws-proto route has no route timeout — an upgraded connection is
// not a request; its idle timeout bounds it.
func (b *builder) policy(
	service string, r *backplanev1.Route, a *routev3.RouteAction, perFilter map[string]*anypb.Any,
) {
	p := r.GetPolicy()

	switch {
	case p.GetTimeout() != nil:
		a.Timeout = durationpb.New(p.GetTimeout().AsDuration())
	case r.GetKind() == backplanev1.RouteKind_ROUTE_KIND_WS_PROTO:
		a.Timeout = durationpb.New(0)
	}

	if p.GetIdleTimeout() != nil {
		a.IdleTimeout = durationpb.New(p.GetIdleTimeout().AsDuration())
	}

	if retry := p.GetRetry(); retry.GetAttempts() > 0 {
		on := strings.Join(retry.GetRetryOn(), ",")
		if on == "" {
			on = defaultRetryOn
		}

		a.RetryPolicy = &routev3.RetryPolicy{RetryOn: on, NumRetries: wrapperspb.UInt32(retry.GetAttempts() - 1)}
		if retry.GetPerTryTimeout() != nil {
			a.RetryPolicy.PerTryTimeout = durationpb.New(retry.GetPerTryTimeout().AsDuration())
		}
	}

	if c := p.GetCors(); len(c.GetOrigins()) > 0 {
		perFilter[filterCORS] = b.any(cors(c))
	}

	if n := p.GetMaxRequestBytes(); n > 0 {
		b.maxRequest(service, r, n, perFilter)
	}
}

// maxRequest buffers the request body up to n bytes (413 above). Only
// HTTP and GraphQL routes: buffering a gRPC or websocket stream would
// break it — the service's own message limit applies there.
func (b *builder) maxRequest(service string, r *backplanev1.Route, n uint64, perFilter map[string]*anypb.Any) {
	switch r.GetKind() {
	case backplanev1.RouteKind_ROUTE_KIND_HTTP, backplanev1.RouteKind_ROUTE_KIND_GRAPHQL:
	default:
		b.warnf("%s: route %s: max_request_bytes is not enforced on %v routes", service, r.GetPrefix(), r.GetKind())

		return
	}

	if n > math.MaxUint32 {
		n = math.MaxUint32
	}

	perFilter[filterBuffer] = b.any(&bufferv3.BufferPerRoute{Override: &bufferv3.BufferPerRoute_Buffer{
		Buffer: &bufferv3.Buffer{MaxRequestBytes: wrapperspb.UInt32(uint32(n))},
	}})
}

// cors: origins as exact strings; "*" is any origin (Envoy's CORS filter
// accepts every origin when a matcher matches "*").
func cors(c *backplanev1.Cors) *corsv3.CorsPolicy {
	origins := make([]*matcherv3.StringMatcher, 0, len(c.GetOrigins()))
	for _, o := range c.GetOrigins() {
		origins = append(origins, &matcherv3.StringMatcher{MatchPattern: &matcherv3.StringMatcher_Exact{Exact: o}})
	}

	out := &corsv3.CorsPolicy{
		AllowOriginStringMatch: origins,
		AllowMethods:           strings.Join(c.GetMethods(), ","),
		AllowHeaders:           strings.Join(c.GetHeaders(), ","),
		ExposeHeaders:          strings.Join(c.GetExposeHeaders(), ","),
		AllowCredentials:       wrapperspb.Bool(c.GetCredentials()),
	}

	if c.GetMaxAge() != nil {
		out.MaxAge = strconv.FormatInt(int64(c.GetMaxAge().AsDuration().Seconds()), 10)
	}

	return out
}

// transcoder is the REST-JSON transcoding of a Connect route: the route's
// own descriptors, else the manifest's shared set; POST
// /<service>/<Method> with a JSON body maps to the method (auto_mapping),
// and google.api.http paths under the route's prefix map too. Nil — the
// route keeps gRPC and gRPC-Web only — when the descriptors are missing or
// lack the route's services: Envoy would reject the whole route table.
func (b *builder) transcoder(
	service string, m *backplanev1.Manifest, r *backplanev1.Route,
) *transcoderv3.GrpcJsonTranscoder {
	raw := r.GetDescriptors()
	if raw == nil {
		raw = m.GetDescriptors()
	}

	if err := describes(raw, r.GetServices()); err != "" {
		b.warnf("%s: route %s: no JSON transcoding: %s", service, r.GetPrefix(), err)

		return nil
	}

	return &transcoderv3.GrpcJsonTranscoder{
		DescriptorSet:     &transcoderv3.GrpcJsonTranscoder_ProtoDescriptorBin{ProtoDescriptorBin: raw},
		Services:          r.GetServices(),
		AutoMapping:       true,
		ConvertGrpcStatus: true,
		PrintOptions:      &transcoderv3.GrpcJsonTranscoder_PrintOptions{AlwaysPrintPrimitiveFields: true},
	}
}

// describes checks that raw is a FileDescriptorSet defining services;
// it reports why not.
func describes(raw []byte, services []string) string {
	if len(raw) == 0 || len(services) == 0 {
		return "no descriptors or services"
	}

	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		return "descriptors: " + err.Error()
	}

	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return "descriptors: " + err.Error()
	}

	for _, s := range services {
		d, err := files.FindDescriptorByName(protoreflect.FullName(s))
		if _, ok := d.(protoreflect.ServiceDescriptor); err != nil || !ok {
			return "descriptors lack service " + s
		}
	}

	return ""
}
