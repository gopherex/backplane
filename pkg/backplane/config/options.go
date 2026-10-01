package config

import (
	"errors"
	"time"

	"github.com/gopherex/xconf"
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
	remote     RemoteFactory
	backoffMin time.Duration
	backoffMax time.Duration
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

// Source appends layers above file and environment, below Consul KV, in
// order.
func Source(sources ...xconf.Source) Option {
	return func(s *settings) { s.extra = append(s.extra, sources...) }
}

// WithoutConsul keeps Open off Consul KV even when the block names it.
func WithoutConsul() Option { return func(s *settings) { s.noConsul = true } }

// RemoteSource is a watched configuration layer carrying a console revision.
type RemoteSource interface {
	xconf.Source
	LastRevision() uint64
}

// RemoteFactory creates the remote layer after file and environment are loaded.
type RemoteFactory func(Backplane, string) (RemoteSource, error)

// WithRemote installs a remote configuration driver. The runtime restricts it
// to Live fields and preserves its last valid values during an outage.
func WithRemote(factory RemoteFactory) Option {
	return func(s *settings) { s.remote = factory }
}

// ConsulBackoff bounds the backoff between retries of an unreachable Consul
// (default 1s..30s).
func ConsulBackoff(minDelay, maxDelay time.Duration) Option {
	return func(s *settings) { s.backoffMin, s.backoffMax = minDelay, maxDelay }
}

// Remote driver configuration errors.
var (
	ErrRemoteDriver = errors.New("config: consul configured without a remote configuration driver")
	ErrRemoteSource = errors.New("config: remote configuration driver returned nil")
)
