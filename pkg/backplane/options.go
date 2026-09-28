package backplane

import (
	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/config"
)

// Option configures Open.
type Option func(*options)

type options struct {
	id     Identity
	log    *xlog.Logger
	config []config.Option
	slog   bool
}

// Name overrides build.Service.
func Name(n string) Option { return func(o *options) { o.id.Service = n } }

// Version overrides build.Version.
func Version(v string) Option { return func(o *options) { o.id.Version = v } }

// Instance overrides the instance id (default: the block's instance, then
// <service>-<hostname>).
func Instance(id string) Option { return func(o *options) { o.id.Instance = id } }

// Advertise overrides the address registered in Consul.
func Advertise(addr string) Option { return func(o *options) { o.id.Advertise = addr } }

// Logger replaces the default logger.
func Logger(l *xlog.Logger) Option { return func(o *options) { o.log = l } }

// ConfigOptions pass options to configuration loading.
func ConfigOptions(opts ...config.Option) Option {
	return func(o *options) { o.config = append(o.config, opts...) }
}

// KeepSlog leaves the log/slog default alone. By default Run routes it into
// the service logger (the OTel SDK and other libraries log through it) and
// restores it when Run returns.
func KeepSlog() Option { return func(o *options) { o.slog = false } }
