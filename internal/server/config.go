// Package server assembles the backplane server: its configuration and the
// State the SDK builds from it. backplane is a service on its own SDK
// (§12.2): the SDK block (BACKPLANE_*, §4.3) plus the sections here, whose
// environment names are BACKPLANE_<PATH> too — the service is named
// "backplane", so its prefix and the block's coincide, and no section here
// shares a name with a block field.
package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	liveconfig "github.com/gopherex/backplane/internal/config"
	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/internal/xds"
	"github.com/gopherex/backplane/pkg/backplane/config"
)

// Name of the service.
const Name = "backplane"

// Config of the backplane server.
type Config struct {
	config.Backplane `json:"backplane"`

	// PostgreSQL, schema `backplane`.
	PG PG `json:"pg"`
	// xDS (ADS) for Envoy.
	XDS XDS `json:"xds"`
	// Console HTTP: `/`, `/ws`, `/auth`, `/plugins`, served behind Envoy.
	Console Console `json:"console"`
	// Admin token of the console (§11.3): the secret /auth/login accepts,
	// set in the deployment; required, at least 16 characters. Changing it
	// is changing the secret and rolling the replicas; sessions opened with
	// the old one last until they expire or are revoked.
	AdminToken config.Secret `json:"admin_token,omitempty"`
	// Observability backends the console links and proxies to (§15.2).
	Obs Obs `json:"obs"`
	// Live-value overrides: BACKPLANE_LIVE_CONFIG_* (§5.3).
	LiveConfig liveconfig.Settings `json:"live_config"`
}

// PG is the store's connection: BACKPLANE_PG_DSN.
type PG struct {
	DSN config.Secret `json:"dsn,omitempty"`
}

// XDS is the control plane Envoy connects to: BACKPLANE_XDS_*.
type XDS struct {
	Listen string `json:"listen" schemapb:"default=:18000"`
	// HTTPPort is the port of Envoy's public HTTP listener the snapshot
	// describes (Envoy binds it, not backplane).
	HTTPPort int64 `json:"http_port" schemapb:"default=10000;gte=1;lte=65535"`
}

// Console of the installation (§11): BACKPLANE_CONSOLE_*.
type Console struct {
	Listen string `json:"listen" schemapb:"default=:8081"`
	// The console lives on its own host (console.example.com) or under a
	// prefix of a shared one (/backplane); one of them, or neither — then it
	// takes `/` of the default virtual host.
	Host   string `json:"host,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	// Origins allowed to open /ws (https://console.example.com); empty:
	// the console's own host.
	Origins []string `json:"origins,omitempty"`
	// Proxies whose X-Forwarded-For is trusted: addresses or CIDRs.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	// InsecureCookie drops Secure from the session cookie (and HSTS):
	// plain-HTTP development only.
	InsecureCookie bool `json:"insecure_cookie" schemapb:"default=false"`
}

// Settings of the console component.
func (c Config) consoleSettings() console.Settings {
	return console.Settings{
		Listen: c.Console.Listen, Host: c.Console.Host, Prefix: c.Console.Prefix,
		Origins: c.Console.Origins, TrustedProxies: c.Console.TrustedProxies, InsecureCookie: c.Console.InsecureCookie,
		AdminToken: c.AdminToken, InternalSecret: c.InternalSecret,
	}
}

// Obs are the observability backends: BACKPLANE_OBS_*; empty: not shown.
type Obs struct {
	MetricsURL string `json:"metrics_url,omitempty"`
	LogsURL    string `json:"logs_url,omitempty"`
	TracesURL  string `json:"traces_url,omitempty"`
}

// Errors of Validate.
var (
	ErrNoPG         = errors.New("pg.dsn is required (BACKPLANE_PG_DSN)")
	ErrNoConsul     = errors.New("backplane.consul.addr is required (BACKPLANE_CONSUL_ADDR)")
	ErrAddr         = errors.New("want host:port with a port in 1..65535")
	ErrPortTaken    = errors.New("port is used twice")
	ErrHostOrPrefix = errors.New("console: host and prefix exclude each other")
	ErrPrefix       = errors.New("console.prefix: want /path without a trailing slash")
	ErrOrigin       = errors.New("console.origins: want scheme://host[:port]")
	ErrProxy        = errors.New("console.trusted_proxies: want an address or CIDR")
	ErrURL          = errors.New("want an absolute http(s) URL")
	ErrNoAdminToken = errors.New("admin_token is required (BACKPLANE_ADMIN_TOKEN): " +
		"the console's login secret, set in the deployment")
	ErrShortAdminToken = fmt.Errorf("want at least %d characters", console.MinAdminTokenLen)
)

// Validate checks what the schema cannot: required connections, listen
// addresses and their ports, the console's placement and lists.
func (c Config) Validate() error {
	var errs []error

	if c.PG.DSN.Reveal() == "" {
		errs = append(errs, ErrNoPG)
	}

	if !c.Consul.Enabled() {
		errs = append(errs, ErrNoConsul)
	}

	if err := c.adminToken(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.ports()...)
	errs = append(errs, c.Console.validate()...)

	for name, u := range map[string]string{
		"obs.metrics_url": c.Obs.MetricsURL, "obs.logs_url": c.Obs.LogsURL, "obs.traces_url": c.Obs.TracesURL,
	} {
		if u != "" && !httpURL(u) {
			errs = append(errs, fmt.Errorf("%s %q: %w", name, u, ErrURL))
		}
	}

	return errors.Join(errs...)
}

// adminToken: the console's admin token is set and long enough.
func (c Config) adminToken() error {
	switch token := c.AdminToken.Reveal(); {
	case token == "":
		return ErrNoAdminToken
	case len(token) < console.MinAdminTokenLen:
		return fmt.Errorf("admin_token (BACKPLANE_ADMIN_TOKEN): %w", ErrShortAdminToken)
	}

	return nil
}

// ports: the listen addresses parse, and no two listeners of the process
// share a port.
func (c Config) ports() []error {
	var errs []error

	used := map[int]string{
		int(c.InternalPort): "backplane.internal_port",
		int(c.PublicPort):   "backplane.public_port",
	}

	for _, l := range []struct{ name, addr string }{
		{"xds.listen", c.XDS.Listen}, {"console.listen", c.Console.Listen},
	} {
		port, err := listenPort(l.addr)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", l.name, l.addr, err))

			continue
		}

		if other, ok := used[port]; ok {
			errs = append(errs, fmt.Errorf("%s: %d is %s's: %w", l.name, port, other, ErrPortTaken))

			continue
		}

		used[port] = l.name
	}

	return errs
}

func (c Console) validate() []error {
	var errs []error

	if c.Host != "" && c.Prefix != "" {
		errs = append(errs, ErrHostOrPrefix)
	}

	if c.Prefix != "" && (!strings.HasPrefix(c.Prefix, "/") || strings.HasSuffix(c.Prefix, "/")) {
		errs = append(errs, fmt.Errorf("%q: %w", c.Prefix, ErrPrefix))
	}

	for _, o := range c.Origins {
		if u, err := url.Parse(o); err != nil || !httpURL(o) || (u.Path != "" && u.Path != "/") {
			errs = append(errs, fmt.Errorf("%q: %w", o, ErrOrigin))
		}
	}

	for _, p := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(p); err == nil {
			continue
		}

		if _, err := netip.ParseAddr(p); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", p, ErrProxy))
		}
	}

	return errs
}

// listenPort is the port of a listen address (":18000", "0.0.0.0:18000").
func listenPort(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, ErrAddr
	}

	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return 0, ErrAddr
	}

	return port, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)

	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// xdsConfig is the xDS server's configuration: its listener, Envoy's
// public listener and the console's routes, which lead to the console
// listener of every healthy backplane instance (this one's advertise
// address while the catalog lists none).
func xdsConfig(c Config, advertise string) xds.Config {
	port, _ := listenPort(c.Console.Listen) // Validate checked it

	return xds.Config{
		Listen: c.XDS.Listen,
		Gateway: xds.Gateway{
			Port: uint32(c.XDS.HTTPPort), //nolint:gosec // 1..65535 by the schema
			Console: xds.Console{
				Host: c.Console.Host, Prefix: c.Console.Prefix,
				Port: uint32(port), Service: Name, Fallback: advertise, //nolint:gosec // a port
			},
		},
	}
}
