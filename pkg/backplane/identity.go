package backplane

import (
	"context"
	"net"
	"os"

	"go.opentelemetry.io/otel/log/global"

	"github.com/gopherex/xlog"
	xlogtrace "github.com/gopherex/xtrace/contrib/libs/xlog"

	"github.com/gopherex/backplane/pkg/backplane/config"
)

const loopback = "127.0.0.1"

// Identity is how the instance names itself everywhere: logs, telemetry,
// Consul, the manifest.
type Identity struct {
	Service     string
	Version     string
	Instance    string
	Advertise   string
	Environment string
}

// resolve fills the identity from options, then the SDK block, then the host.
func (id Identity) resolve(ctx context.Context, cfg config.Backplane) Identity {
	id.Instance = first(id.Instance, cfg.Instance)
	if id.Instance == "" {
		host, _ := os.Hostname()
		id.Instance = id.Service + "-" + host
	}

	id.Advertise = first(id.Advertise, cfg.Advertise, os.Getenv("POD_IP"))
	if id.Advertise == "" {
		id.Advertise = hostAddress(ctx)
	}

	id.Version = first(id.Version, "0.0.0")
	id.Environment = cfg.Environment

	return id
}

// logger is the given one or JSON to stdout teed into the OpenTelemetry
// logs pipeline (a no-op until telemetry exports logs), with trace fields
// and span observation; every record carries the identity.
func (id Identity) logger(given *xlog.Logger, cfg config.Backplane) *xlog.Logger {
	log := given
	if log == nil {
		level, err := xlog.ParseLevel(cfg.LogLevel.Get())

		stdout := xlog.NewJSON(xlog.WithLevel(level)).Core()
		otlp := xlogtrace.Core(global.GetLoggerProvider().Logger(id.Service))
		log = xlog.NewJSON(append(xlogtrace.Options(),
			xlog.WithLevel(level), xlog.WithCore(xlog.NewTeeCore(stdout, otlp)))...)

		if err != nil {
			log.Warn("log level", xlog.Err(err), xlog.String("using", level.String()))
		}
	}

	return log.With(
		xlog.String("service", id.Service), xlog.String("instance", id.Instance), xlog.String("version", id.Version),
	)
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// hostAddress is the first non-loopback IPv4 of the hostname.
func hostAddress(ctx context.Context) string {
	host, err := os.Hostname()
	if err != nil {
		return loopback
	}

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return loopback
	}

	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && !ip.IsLoopback() && ip.To4() != nil {
			return a
		}
	}

	return loopback
}
