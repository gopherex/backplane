// Package routes holds what route options configure, for the SDK core.
package routes

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/apifiles"
)

// Errors.
var (
	// ErrPrefix: an HTTP path prefix the SDK cannot announce consistently.
	ErrPrefix = errors.New("route: bad path prefix")
	// ErrPolicy: an Envoy policy Envoy would reject.
	ErrPolicy = errors.New("route: bad policy")
)

// Managed is a route the SDK serves.
type Managed struct {
	Listen      string
	Host        string
	Transcode   bool   // gRPC: Connect (gRPC-Web, REST-JSON) at Envoy
	OpenAPI     []byte // HTTP
	SchemaFiles *SchemaFiles
	// Envoy policy; nil takes the platform defaults.
	Policy *backplanev1.RoutePolicy
	// gRPC and ws-proto: interceptors of this registration only.
	Unary  []grpc.UnaryServerInterceptor
	Stream []grpc.StreamServerInterceptor
	// HTTP and ws-proto: wraps this route's handler; the first is outermost.
	Middleware []func(http.Handler) http.Handler
	// gRPC: serve grpc.reflection on the route's port.
	Reflection bool
}

// SchemaFiles is a file snapshot and its manifest reference (or a declaration error).
type SchemaFiles struct {
	Files *apifiles.Files
	Ref   *backplanev1.APISchemaBundle
	Err   error
}

// PolicyOf returns p, allocating it when nil.
func PolicyOf(p **backplanev1.RoutePolicy) *backplanev1.RoutePolicy {
	if *p == nil {
		*p = &backplanev1.RoutePolicy{}
	}

	return *p
}

// Wrap applies the middleware to h, the first one outermost.
func (m Managed) Wrap(h http.Handler) http.Handler {
	for i := len(m.Middleware) - 1; i >= 0; i-- {
		h = m.Middleware[i](h)
	}

	return h
}

// Prefix normalizes an HTTP path prefix so the Go mux and Envoy agree: it
// must start with "/", carry no method, host or wildcard, and ends with "/"
// ("/api" serves "/api/..." and redirects "/api").
func Prefix(p string) (string, error) {
	switch {
	case !strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("%w %q: must start with /", ErrPrefix, p)
	case strings.ContainsAny(p, " \t{}"):
		return "", fmt.Errorf("%w %q: no method, host or wildcard, only a path", ErrPrefix, p)
	case !strings.HasSuffix(p, "/"):
		p += "/"
	}

	return p, nil
}

// CheckPolicy rejects what Envoy would: negative durations, a retry
// without attempts, CORS without origins.
func CheckPolicy(p *backplanev1.RoutePolicy) error {
	if p == nil {
		return nil
	}

	durations := []struct {
		name string
		d    *durationpb.Duration
	}{
		{"timeout", p.GetTimeout()},
		{"idle_timeout", p.GetIdleTimeout()},
		{"retry.per_try_timeout", p.GetRetry().GetPerTryTimeout()},
		{"cors.max_age", p.GetCors().GetMaxAge()},
	}

	for _, f := range durations {
		if f.d != nil && f.d.AsDuration() < 0 {
			return fmt.Errorf("%w: %s must be >= 0, got %v", ErrPolicy, f.name, f.d.AsDuration())
		}
	}

	switch {
	case p.GetRetry() != nil && p.GetRetry().GetAttempts() == 0:
		return fmt.Errorf("%w: retry needs at least one attempt", ErrPolicy)
	case p.GetCors() != nil && len(p.GetCors().GetOrigins()) == 0:
		return fmt.Errorf("%w: cors needs at least one origin", ErrPolicy)
	}

	return nil
}
