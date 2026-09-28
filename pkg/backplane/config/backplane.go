package config

import (
	"fmt"
	"time"

	"github.com/hashicorp/consul/api"
)

// Backplane is the SDK's own block, part of the service configuration:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    DB string        `json:"db"`
//	}
//
// Its environment names are fixed (BACKPLANE_*) for every service. Every
// dependency is optional for a service: an empty address means "without it".
type Backplane struct {
	Consul   Consul   `json:"consul"   schemapb:"default={}"`
	NATS     NATS     `json:"nats"     schemapb:"default={}"`
	Temporal Temporal `json:"temporal" schemapb:"default={}"`
	Shutdown Shutdown `json:"shutdown" schemapb:"default={}"`

	// Instance id; default <service>-<hostname>.
	Instance string `json:"instance,omitempty"`
	// Address other components reach this instance at; default POD_IP, then
	// the hostname's address.
	Advertise string `json:"advertise,omitempty"`
	// deployment.environment.name for telemetry; exporters use OTEL_*.
	Environment string `json:"environment,omitempty"`
	// Platform port: internal API, health, probes, UI bundle.
	InternalPort int64 `json:"internal_port" schemapb:"default=9400;gte=1;lte=65535"`
	// Shared secret the console relay presents; empty disables the check.
	InternalSecret Secret `json:"internal_secret,omitempty"`
	// Default listener of managed public routes.
	PublicPort int64  `json:"public_port" schemapb:"default=8080;gte=1;lte=65535"`
	LogLevel   string `json:"log_level"   schemapb:"default=info"`
}

// Backplaner is satisfied by any struct embedding Backplane.
type Backplaner interface{ BackplaneConfig() Backplane }

// BackplaneConfig returns the block; promoted to the embedding struct.
func (b Backplane) BackplaneConfig() Backplane { return b }

// Consul connection.
type Consul struct {
	Addr  string `json:"addr,omitempty"`
	Token Secret `json:"token,omitempty"`
	// false when the deployment registers the service itself.
	Register bool `json:"register" schemapb:"default=true"`
}

// Enabled reports whether Consul is configured.
func (c Consul) Enabled() bool { return c.Addr != "" }

// Client builds a Consul API client.
func (c Consul) Client() (*api.Client, error) {
	client, err := api.NewClient(&api.Config{Address: c.Addr, Token: c.Token.Reveal()})
	if err != nil {
		return nil, fmt.Errorf("consul client %s: %w", c.Addr, err)
	}

	return client, nil
}

// NATS connection.
type NATS struct {
	URL   string `json:"url,omitempty"`
	Creds Secret `json:"creds,omitempty"`
}

// Enabled reports whether NATS is configured.
func (n NATS) Enabled() bool { return n.URL != "" }

// Temporal connection. Field is `ns`: `namespace` is a CEL reserved word.
type Temporal struct {
	Addr      string `json:"addr,omitempty"`
	Namespace string `json:"ns"             schemapb:"default=default"`
}

// Enabled reports whether Temporal is configured.
func (t Temporal) Enabled() bool { return t.Addr != "" }

// Shutdown budget.
type Shutdown struct {
	Timeout time.Duration `json:"timeout" schemapb:"default=25s"`
}
