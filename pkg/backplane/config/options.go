package config

import (
	"time"

	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"
)

// Option configures Load and Open.
type Option func(*settings)

type settings struct {
	service    string
	file       string
	fileSet    bool
	noFile     bool
	envPrefix  string
	prefixSet  bool
	noEnv      bool
	noConsul   bool
	extra      []xconf.Source
	consulOpts []consulsrc.Option
	retryEvery time.Duration
}

// Service names the service when build.Service is not stamped.
func Service(name string) Option { return func(s *settings) { s.service = name } }

// File reads this file (JSON or YAML by extension) instead of
// BACKPLANE_CONFIG_FILE.
func File(path string) Option { return func(s *settings) { s.file, s.fileSet = path, true } }

// WithoutFile disables the file layer.
func WithoutFile() Option { return func(s *settings) { s.noFile = true } }

// EnvPrefix replaces the derived prefix <SERVICE>_.
func EnvPrefix(prefix string) Option {
	return func(s *settings) { s.envPrefix, s.prefixSet = prefix, true }
}

// WithoutEnv disables the environment layer.
func WithoutEnv() Option { return func(s *settings) { s.noEnv = true } }

// Source appends layers above the standard ones, in order.
func Source(sources ...xconf.Source) Option {
	return func(s *settings) { s.extra = append(s.extra, sources...) }
}

// WithoutConsul keeps Open off Consul KV even when the block names it.
func WithoutConsul() Option { return func(s *settings) { s.noConsul = true } }

// ConsulOptions pass through to the xconf Consul source (Open only).
func ConsulOptions(opts ...consulsrc.Option) Option {
	return func(s *settings) { s.consulOpts = append(s.consulOpts, opts...) }
}

// RetryEvery sets how often Open retries an unreachable Consul (default 30s).
func RetryEvery(d time.Duration) Option { return func(s *settings) { s.retryEvery = d } }
