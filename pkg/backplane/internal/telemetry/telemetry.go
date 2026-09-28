// Package telemetry installs OpenTelemetry through xtrace's SDK bootstrap:
// resource from the build identity, exporters from the standard OTEL_*
// environment. Without an OTLP endpoint export stays off (one warning), so a
// service never retries a collector that does not exist.
package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/attribute"

	"github.com/gopherex/xlog"
	xsdk "github.com/gopherex/xtrace/contrib/sdk"

	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
)

// Identity stamped into the resource.
type Identity struct {
	Service, Version, Instance, Environment string
}

// Telemetry is a lifecycle component; start it first, it stops last.
type Telemetry struct {
	id       Identity
	log      *xlog.Logger
	shutdown xsdk.Shutdown
}

// New creates the component.
func New(id Identity, log *xlog.Logger) *Telemetry { return &Telemetry{id: id, log: log} }

// Name implements lifecycle.Component.
func (t *Telemetry) Name() string { return "telemetry" }

// Start installs global providers and the propagator.
func (t *Telemetry) Start(ctx context.Context, _ lifecycle.Group) error {
	opts := []xsdk.Option{
		xsdk.WithService(t.id.Service),
		xsdk.WithVersion(t.id.Version),
		xsdk.WithInstanceID(t.id.Instance),
	}
	if t.id.Environment != "" {
		opts = append(opts, xsdk.WithAttributes(attribute.String("deployment.environment.name", t.id.Environment)))
	}

	export := exporting()
	if !export {
		t.log.Warn("telemetry export off: OTEL_EXPORTER_OTLP_ENDPOINT not set")

		opts = append(opts, xsdk.WithoutTraces(), xsdk.WithoutMetrics(), xsdk.WithoutLogs())
	}

	shutdown, err := xsdk.Setup(ctx, opts...)
	if err != nil {
		t.log.Warn("telemetry setup failed, continuing without export", xlog.Err(err))
		return nil
	}

	t.shutdown = shutdown

	if export {
		if err := xsdk.StartHostRuntime(); err != nil {
			t.log.Warn("runtime metrics", xlog.Err(err))
		}
	}

	return nil
}

// Stop flushes and shuts the providers down.
func (t *Telemetry) Stop(ctx context.Context) error {
	if t.shutdown == nil {
		return nil
	}

	return t.shutdown(ctx)
}

// exporting reports whether any OTLP endpoint is configured.
func exporting() bool {
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	} {
		if os.Getenv(name) != "" {
			return true
		}
	}

	return false
}
