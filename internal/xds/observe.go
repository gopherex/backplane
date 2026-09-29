package xds

import (
	"context"
	"fmt"
	"strconv"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	serverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/genproto/googleapis/rpc/status"

	"github.com/gopherex/xlog"
)

// Metrics of the component (on its node's meter):
//
//	backplane.xds.snapshot.version  gauge: the served snapshot
//	backplane.xds.services          gauge: services with routes
//	backplane.xds.routes            gauge: service routes placed
//	backplane.xds.clusters          gauge
//	backplane.xds.endpoints         gauge: endpoints over all clusters
//	backplane.xds.streams           up-down: open ADS streams
//	backplane.xds.errors{stage}     counter: build | snapshot | nack
type metrics struct {
	version   metric.Int64Gauge
	services  metric.Int64Gauge
	routes    metric.Int64Gauge
	clusters  metric.Int64Gauge
	endpoints metric.Int64Gauge
	streams   metric.Int64UpDownCounter
	errors    metric.Int64Counter
}

func newMetrics(m metric.Meter) metrics {
	// Creation only fails on invalid names, fixed here.
	version, _ := m.Int64Gauge("backplane.xds.snapshot.version")
	services, _ := m.Int64Gauge("backplane.xds.services")
	routes, _ := m.Int64Gauge("backplane.xds.routes")
	clusters, _ := m.Int64Gauge("backplane.xds.clusters")
	endpoints, _ := m.Int64Gauge("backplane.xds.endpoints")
	streams, _ := m.Int64UpDownCounter("backplane.xds.streams")
	errs, _ := m.Int64Counter("backplane.xds.errors")

	return metrics{
		version: version, services: services, routes: routes, clusters: clusters,
		endpoints: endpoints, streams: streams, errors: errs,
	}
}

func (m metrics) snapshot(ctx context.Context, version uint64, res Resources) {
	eps := 0

	for _, r := range res.Endpoints {
		if cla, ok := r.(*endpointv3.ClusterLoadAssignment); ok {
			for _, l := range cla.GetEndpoints() {
				eps += len(l.GetLbEndpoints())
			}
		}
	}

	m.version.Record(ctx, int64(version)) //nolint:gosec // a counter of snapshots
	m.services.Record(ctx, int64(res.ServiceCount))
	m.routes.Record(ctx, int64(res.RouteCount))
	m.clusters.Record(ctx, int64(len(res.Clusters)))
	m.endpoints.Record(ctx, int64(eps))
}

func (m metrics) error(ctx context.Context, stage string) {
	m.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage)))
}

// callbacks log Envoys connecting and leaving, and every NACK: a resource
// Envoy rejected keeps its previous version there.
func (s *Server) callbacks() serverv3.CallbackFuncs {
	opened := func(ctx context.Context, _ int64, _ string) error {
		s.metrics.streams.Add(ctx, 1)

		return nil
	}
	closed := func(id int64, _ *corev3.Node) {
		s.metrics.streams.Add(context.Background(), -1)

		s.mu.Lock()
		node := s.nodes[id]
		delete(s.nodes, id)
		s.mu.Unlock()

		if node != nil {
			s.Log().Info("envoy disconnected",
				xlog.String("envoy", node.GetId()), xlog.String("envoy_cluster", node.GetCluster()))
		}
	}

	return serverv3.CallbackFuncs{
		StreamOpenFunc:        opened,
		DeltaStreamOpenFunc:   opened,
		StreamClosedFunc:      closed,
		DeltaStreamClosedFunc: closed,
		StreamRequestFunc: func(id int64, req *discoveryv3.DiscoveryRequest) error {
			s.request(id, req.GetNode(), req.GetTypeUrl(), req.GetVersionInfo(), req.GetErrorDetail())

			return nil
		},
		StreamDeltaRequestFunc: func(id int64, req *discoveryv3.DeltaDiscoveryRequest) error {
			s.request(id, req.GetNode(), req.GetTypeUrl(), "", req.GetErrorDetail())

			return nil
		},
	}
}

// request notes the stream's node (sent on its first request) and NACKs.
func (s *Server) request(id int64, node *corev3.Node, typeURL, version string, nack *status.Status) {
	s.mu.Lock()
	known, seen := s.nodes[id]

	if !seen && node != nil {
		s.nodes[id], known = node, node
	}
	s.mu.Unlock()

	if !seen && node != nil {
		s.Log().Info("envoy connected",
			xlog.String("envoy", node.GetId()), xlog.String("envoy_cluster", node.GetCluster()),
			xlog.String("stream", strconv.FormatInt(id, 10)))
	}

	if nack == nil {
		return
	}

	s.metrics.error(context.Background(), "nack")
	s.Log().Error("envoy rejected xds resources",
		xlog.String("envoy", known.GetId()), xlog.String("type", typeURL),
		xlog.String("snapshot", version), xlog.String("error", nack.GetMessage()))
}

// logger adapts the node's logger to go-control-plane's.
type logger struct{ log *xlog.Logger }

func (l logger) Debugf(format string, args ...any) { l.log.Debug(fmt.Sprintf(format, args...)) }
func (l logger) Infof(format string, args ...any)  { l.log.Debug(fmt.Sprintf(format, args...)) }
func (l logger) Warnf(format string, args ...any)  { l.log.Warn(fmt.Sprintf(format, args...)) }
func (l logger) Errorf(format string, args ...any) { l.log.Error(fmt.Sprintf(format, args...)) }
