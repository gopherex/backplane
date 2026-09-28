// Package listener serves gRPC and HTTP on one address through cmux. The
// platform port and every public port are this component.
package listener

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

const readHeaderTimeout = 10 * time.Second

// Listener serves one address. Either server may be nil.
type Listener struct {
	name string
	addr string
	grpc *grpc.Server
	http *http.Server
	log  *xlog.Logger
	ln   *tracking
}

// New creates a listener; nothing is bound until Start.
func New(name, addr string, g *grpc.Server, h http.Handler, log *xlog.Logger) *Listener {
	l := &Listener{name: name, addr: addr, grpc: g, log: log}
	if h != nil {
		l.http = &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout}
	}

	return l
}

// Start binds the address and serves through the lifecycle group.
func (l *Listener) Start(ctx context.Context, g node.Group) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", l.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", l.addr, err)
	}

	l.ln = &tracking{Listener: ln, conns: map[*trackedConn]struct{}{}}

	m := cmux.New(l.ln)
	// A connection that never sends a byte is dropped after the timeout;
	// Stop closes those still being classified so m.Serve returns at once.
	m.SetReadTimeout(readHeaderTimeout)

	if l.grpc != nil {
		gl := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))

		// Serve fails with cmux.ErrServerClosed when the shared socket closes
		// before GracefulStop marks the server as stopping.
		g.Go(func(context.Context) error { return ignoreClosed(l.grpc.Serve(gl)) })
	}

	if l.http != nil {
		hl := m.Match(cmux.Any())

		g.Go(func(context.Context) error { return ignoreClosed(l.http.Serve(hl)) })
	}

	g.Go(func(context.Context) error { return ignoreClosed(m.Serve()) })
	l.log.Info("listening", xlog.String("listener", l.name), xlog.String("addr", ln.Addr().String()))

	return nil
}

// Addr is the bound address; empty before Start.
func (l *Listener) Addr() string {
	if l.ln == nil {
		return ""
	}

	return l.ln.Addr().String()
}

// Stop drains gRPC and HTTP together within ctx, then closes the socket and
// every connection still open: ones cmux is still classifying (an idle
// client would otherwise hold m.Serve for the read timeout), hijacked ones
// and ones that outlived the drain budget.
func (l *Listener) Stop(ctx context.Context) error {
	var (
		drain sync.WaitGroup
		herr  error
	)

	if l.grpc != nil {
		drain.Go(func() {
			done := make(chan struct{})

			go func() { l.grpc.GracefulStop(); close(done) }()

			select {
			case <-done:
			case <-ctx.Done():
				l.grpc.Stop()
			}
		})
	}

	if l.http != nil {
		// Shutdown closes the cmux listener, which is the shared socket:
		// whoever closes it second sees it closed already.
		drain.Go(func() { herr = ignoreClosed(l.http.Shutdown(ctx)) })
	}

	drain.Wait()

	errs := []error{herr}
	if l.ln != nil {
		errs = append(errs, ignoreClosed(l.ln.Close()))
		l.ln.closeAll()
	}

	return errors.Join(errs...)
}

// tracking remembers the connections it accepted until they close.
type tracking struct {
	net.Listener

	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

func (t *tracking) Accept() (net.Conn, error) {
	c, err := t.Listener.Accept()
	if err != nil {
		return nil, err //nolint:wrapcheck // cmux inspects the listener's own errors
	}

	tc := &trackedConn{Conn: c, owner: t}

	t.mu.Lock()
	t.conns[tc] = struct{}{}
	t.mu.Unlock()

	return tc, nil
}

func (t *tracking) closeAll() {
	t.mu.Lock()
	conns := make([]*trackedConn, 0, len(t.conns))

	for c := range t.conns {
		conns = append(conns, c)
	}
	t.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
}

type trackedConn struct {
	net.Conn

	owner *tracking
	once  sync.Once
	err   error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()

		c.err = c.Conn.Close()
	})

	return c.err
}

func ignoreClosed(err error) error {
	closed := errors.Is(err, net.ErrClosed) ||
		errors.Is(err, http.ErrServerClosed) ||
		errors.Is(err, cmux.ErrListenerClosed) ||
		errors.Is(err, cmux.ErrServerClosed)
	if err == nil || closed {
		return nil
	}

	return err
}
