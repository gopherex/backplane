// Package listener serves gRPC and HTTP on one address through cmux. The
// platform port and every public port are this component.
package listener

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/internal/lifecycle"
)

const readHeaderTimeout = 10 * time.Second

// Listener is a lifecycle component. Either server may be nil.
type Listener struct {
	name string
	addr string
	grpc *grpc.Server
	http *http.Server
	log  *xlog.Logger
	ln   net.Listener
}

// New creates a listener; nothing is bound until Start.
func New(name, addr string, g *grpc.Server, h http.Handler, log *xlog.Logger) *Listener {
	l := &Listener{name: name, addr: addr, grpc: g, log: log}
	if h != nil {
		l.http = &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout}
	}

	return l
}

// Name implements lifecycle.Component.
func (l *Listener) Name() string { return l.name }

// Start binds the address and serves through the lifecycle group.
func (l *Listener) Start(ctx context.Context, g lifecycle.Group) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", l.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", l.addr, err)
	}

	l.ln = ln

	m := cmux.New(ln)
	if l.grpc != nil {
		gl := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))

		g.Go(l.name+".grpc", func(context.Context) error { return l.grpc.Serve(gl) })
	}

	if l.http != nil {
		hl := m.Match(cmux.Any())

		g.Go(l.name+".http", func(context.Context) error { return ignoreClosed(l.http.Serve(hl)) })
	}

	g.Go(l.name+".mux", func(context.Context) error { return ignoreClosed(m.Serve()) })
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

// Stop drains gRPC and HTTP within ctx, then closes the socket.
func (l *Listener) Stop(ctx context.Context) error {
	var errs []error

	if l.grpc != nil {
		done := make(chan struct{})

		go func() { l.grpc.GracefulStop(); close(done) }()

		select {
		case <-done:
		case <-ctx.Done():
			l.grpc.Stop()
		}
	}

	if l.http != nil {
		errs = append(errs, l.http.Shutdown(ctx))
	}

	if l.ln != nil {
		errs = append(errs, ignoreClosed(l.ln.Close()))
	}

	return errors.Join(errs...)
}

func ignoreClosed(err error) error {
	closed := errors.Is(err, net.ErrClosed) ||
		errors.Is(err, http.ErrServerClosed) ||
		errors.Is(err, cmux.ErrListenerClosed)
	if err == nil || closed {
		return nil
	}

	return err
}
