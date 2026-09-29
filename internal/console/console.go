// Package console is backplane's console server (§11): the HTTP the shell
// talks to, behind Envoy like any service's routes.
//
//	POST /auth/login     {token} → session cookie (HttpOnly, SameSite=Strict)
//	POST /auth/logout    ends the session
//	GET  /auth/session   the current session, 401 without one
//	GET  /ws             ws-proto: backplane's own API (CatalogService,
//	                     SessionService, and what WithServices adds) and the
//	                     relay of everything else to the services' internal
//	                     API on their platform ports
//	GET  /plugins/<service>/<hash>/<path>
//	                     console plugin bundles, fetched from a live instance
//	                     and cached by content hash
//	GET  /               the shell
//
// The console listens on its own address (Settings.Listen) from the start
// of its node to its stop, under Settings.Prefix (or at "/"): Envoy routes
// the console's host or prefix there (cluster backplane_console, §6) and
// passes the path as is.
package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// Session lifetime (§11.3).
const (
	// SessionTTL is the absolute lifetime of a session.
	SessionTTL = 12 * time.Hour
	// IdleTimeout ends a session without activity (a login, a request with
	// its cookie, an RPC on its connection).
	IdleTimeout = time.Hour

	// touchEvery bounds how often activity is written to the store.
	touchEvery = time.Minute
	// recheckEvery: an open connection re-reads its session this often and
	// closes once it is revoked (on another replica), expired or idle.
	recheckEvery = 30 * time.Second
	// cleanupEvery deletes stale sessions from the store.
	cleanupEvery = 10 * time.Minute
	// Limits of the console's HTTP server.
	readHeaderTimeout = 10 * time.Second
	serverIdleTimeout = 2 * time.Minute
	maxHeaderBytes    = 1 << 20
	// defaultCacheBytes caps the plugin bundle cache.
	defaultCacheBytes = 64 << 20
)

// Settings are the console's part of the server configuration.
type Settings struct {
	// Listen is the console's own listener (":8081"); empty: none (the
	// Handler is served by someone else, e.g. a test server).
	Listen string
	// Host the console is served on (Envoy matches it); empty: any.
	Host string
	// Prefix the console is mounted under ("/backplane"); empty: "/".
	Prefix string
	// Origins allowed to open /ws and to log in (scheme://host[:port]);
	// empty: the request's own host.
	Origins []string
	// TrustedProxies whose X-Forwarded-For names the client (addresses,
	// CIDRs): Envoy's addresses.
	TrustedProxies []string
	// InsecureCookie drops Secure from the session cookie and HSTS from the
	// responses: plain-HTTP development only.
	InsecureCookie bool
	// AdminToken bootstraps the admin token at the first start; empty:
	// one is generated and printed once.
	AdminToken config.Secret
	// InternalSecret is presented to the services' platform ports
	// (relay and bundles).
	InternalSecret config.Secret
}

// Option configures New.
type Option func(o *options)

type options struct {
	now        func() time.Time
	out        io.Writer
	services   []func(grpc.ServiceRegistrar)
	cacheBytes int64
	shell      fs.FS
}

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithTokenOutput is where a generated admin token is printed once
// (default os.Stderr).
func WithTokenOutput(w io.Writer) Option { return func(o *options) { o.out = w } }

// WithServices registers more of backplane's own API on /ws (another
// component's gRPC services, e.g. configuration).
func WithServices(register func(r grpc.ServiceRegistrar)) Option {
	return func(o *options) { o.services = append(o.services, register) }
}

// WithCacheBytes caps the plugin bundle cache (default 64 MiB).
func WithCacheBytes(n int64) Option { return func(o *options) { o.cacheBytes = n } }

// WithShell serves the console's frontend from fsys (index.html for every
// path that is not a file); without it "/" answers a placeholder.
func WithShell(fsys fs.FS) Option { return func(o *options) { o.shell = fsys } }

// Console is the console server: a component whose start writes the admin
// token at the first start of the installation and opens the listener,
// whose goroutine deletes stale sessions and closes relay connections of
// gone instances, and whose stop shuts the listener down and closes the
// open /ws connections.
//
// A Console is shared by pointer: it holds the limiter, the connections,
// the relay pool and the bundle cache.
type Console struct {
	deps.Component

	settings Settings
	base     string // "" or "/prefix"
	origins  []string
	proxies  proxies
	sessions Sessions
	src      registry.Source
	now      func() time.Time
	out      io.Writer

	limiter *limiter
	conns   *connections
	relay   *relay
	bundles *bundles
	handler http.Handler

	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
}

// New creates the console under parent: sessions keep the token and the
// sessions, src is the installation.
func New(parent deps.Scope, s Settings, sessions Sessions, src registry.Source, opts ...Option) *Console {
	o := options{now: time.Now, out: os.Stderr, cacheBytes: defaultCacheBytes}
	for _, opt := range opts {
		opt(&o)
	}

	c := &Console{
		Component: deps.NewComponent(parent, "console"),
		settings:  s,
		base:      strings.TrimSuffix(s.Prefix, "/"),
		proxies:   parseProxies(s.TrustedProxies),
		sessions:  sessions,
		src:       src,
		now:       o.now,
		out:       o.out,
		limiter:   newLimiter(),
		conns:     newConnections(),
	}

	for _, origin := range s.Origins {
		c.origins = append(c.origins, strings.ToLower(strings.TrimSuffix(origin, "/")))
	}

	c.relay = newRelay(src, s.InternalSecret)
	c.bundles = newBundles(s.InternalSecret, o.cacheBytes)
	c.handler = c.routes(c.newWS(o.services), o.shell)

	c.OnStart(c.bootstrap)
	c.OnStart(c.listen)
	c.OnStop(c.shutdown)
	c.Go(c.housekeeping)

	return c
}

// Handler serves the console under Route (the prefix included).
func (c *Console) Handler() http.Handler { return c.handler }

// Route is where the console is mounted: "/" or "<prefix>/".
func (c *Console) Route() string { return c.base + "/" }

// Addr is the listener's address once started; nil without one.
func (c *Console) Addr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ln == nil {
		return nil
	}

	return c.ln.Addr()
}

// listen opens Settings.Listen and serves the console on it.
func (c *Console) listen(ctx context.Context) error {
	if c.settings.Listen == "" {
		return nil
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.settings.Listen)
	if err != nil {
		return fmt.Errorf("console: listen: %w", err)
	}

	srv := &http.Server{
		Handler:           otelhttp.NewHandler(c.handler, "console"),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	c.mu.Lock()
	c.srv, c.ln = srv, ln
	c.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log().Error("console: serve", xlog.Err(err))
		}
	}()

	c.Log().Info("console listening", xlog.String("addr", ln.Addr().String()), xlog.String("route", c.Route()))

	return nil
}

// shutdown stops accepting, closes the open /ws connections (hijacked:
// Shutdown does not see them) and waits for in-flight requests within ctx.
func (c *Console) shutdown(ctx context.Context) error {
	c.mu.Lock()
	srv := c.srv
	c.mu.Unlock()

	if srv == nil {
		return nil
	}

	c.conns.closeAll()

	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()

		return fmt.Errorf("console: shutdown: %w", err)
	}

	return nil
}

// bootstrap writes the admin token at the first start of the
// installation: BACKPLANE_ADMIN_TOKEN, or a generated one printed once. A
// stored token wins over the environment afterwards (it may have been
// rotated from the console).
func (c *Console) bootstrap(ctx context.Context) error {
	env := c.settings.AdminToken.Reveal()

	stored, err := c.sessions.AdminToken(ctx)
	switch {
	case err == nil:
		if env != "" {
			if ok, verr := verifyToken(stored, env); verr != nil || !ok {
				c.Log().Warn("admin_token is ignored: the stored token (rotated from the console) wins; " +
					"delete the row of backplane.console_admin to bootstrap again")
			}
		}

		return nil
	case !errors.Is(err, ErrNoToken):
		return fmt.Errorf("console: bootstrap: %w", err)
	}

	token, generated := env, false
	if token == "" {
		if token, err = newAdminToken(); err != nil {
			return err
		}

		generated = true
	}

	hash, err := hashToken(token)
	if err != nil {
		return err
	}

	won, err := c.sessions.InitAdminToken(ctx, hash)
	if err != nil {
		return fmt.Errorf("console: bootstrap: %w", err)
	}

	if !won {
		return nil // another replica bootstrapped first
	}

	if !generated {
		c.Log().Info("console admin token set from admin_token")

		return nil
	}

	if _, err := fmt.Fprintf(c.out, "\n"+
		"  backplane console admin token (shown once, stored as a hash):\n\n"+
		"      %s\n\n"+
		"  Log in with it, then rotate it from the console if it was seen by anyone else.\n\n", token); err != nil {
		return fmt.Errorf("console: print the admin token: %w", err)
	}

	c.Log().Warn("console admin token generated and printed once to the process output")

	return nil
}

// housekeeping deletes stale sessions and drops relay connections to
// instances that left, until the node stops.
func (c *Console) housekeeping(ctx context.Context) error {
	defer c.relay.close()

	changes := c.src.Changes(ctx)
	cleanup := time.NewTicker(cleanupEvery)

	defer cleanup.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				return nil
			}

			c.relay.prune(c.src.Current())
		case <-cleanup.C:
			now := c.now()
			if _, err := c.sessions.DeleteStale(ctx, now, now.Add(-IdleTimeout)); err != nil && ctx.Err() == nil {
				c.Log().Warn("console: delete stale sessions", xlog.Err(err))
			}
		}
	}
}

// alive reports whether s is usable at now.
func alive(s Session, now time.Time) bool {
	return now.Before(s.ExpiresAt) && now.Sub(s.LastSeenAt) < IdleTimeout
}
