package route

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

// policy sets part of a route's Envoy policy.
type policy func(p *backplanev1.RoutePolicy)

func (f policy) applyGRPC(m *routes.Managed)        { f(routes.PolicyOf(&m.Policy)) }
func (f policy) applyHTTP(m *routes.Managed)        { f(routes.PolicyOf(&m.Policy)) }
func (f policy) applyWS(m *routes.Managed)          { f(routes.PolicyOf(&m.Policy)) }
func (f policy) applyGRPCDecl(r *backplanev1.Route) { f.applyDecl(r) }
func (f policy) applyHTTPDecl(r *backplanev1.Route) { f.applyDecl(r) }
func (f policy) applyDecl(r *backplanev1.Route)     { f(routes.PolicyOf(&r.Policy)) }

// PolicyOption applies to every route, managed and declarative. Values
// Envoy would reject (a negative duration, a retry without attempts, CORS
// without origins) are a Run error.
type PolicyOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
	GRPCDeclOption
	HTTPDeclOption
	DeclOption
}

// Timeout bounds the whole request at Envoy, streams included: set it on
// a streaming route (Envoy's default is 15s); 0 disables it.
func Timeout(d time.Duration) PolicyOption {
	return policy(func(p *backplanev1.RoutePolicy) { p.Timeout = durationpb.New(d) })
}

// IdleTimeout ends a request (or stream) idle for d at Envoy.
func IdleTimeout(d time.Duration) PolicyOption {
	return policy(func(p *backplanev1.RoutePolicy) { p.IdleTimeout = durationpb.New(d) })
}

// Retry lets Envoy retry a failed request: attempts in all (at least 1),
// each bounded by perTry (0: the route's timeout), on the given Envoy
// retry_on conditions ("5xx", "reset", "unavailable", ...; none: the
// platform default).
func Retry(attempts uint32, perTry time.Duration, on ...string) PolicyOption {
	return policy(func(p *backplanev1.RoutePolicy) {
		p.Retry = &backplanev1.RetryPolicy{
			Attempts: attempts, PerTryTimeout: durationpb.New(perTry), RetryOn: append([]string(nil), on...),
		}
	})
}

// CORSPolicy is the browser cross-origin policy Envoy applies to a route.
type CORSPolicy struct {
	// Allowed origins, exact ("https://app.example.com") or "*"; at least one.
	Origins []string
	// Allowed methods; none: the platform default.
	Methods []string
	// Allowed request headers.
	Headers []string
	// Response headers the browser may read.
	ExposeHeaders []string
	// Credentials (cookies, Authorization) allowed.
	Credentials bool
	// How long a browser caches the preflight; 0: the platform default.
	MaxAge time.Duration
}

// CORS sets the route's cross-origin policy.
func CORS(c CORSPolicy) PolicyOption {
	cors := &backplanev1.Cors{
		Origins:       append([]string(nil), c.Origins...),
		Methods:       append([]string(nil), c.Methods...),
		Headers:       append([]string(nil), c.Headers...),
		ExposeHeaders: append([]string(nil), c.ExposeHeaders...),
		Credentials:   c.Credentials,
	}
	if c.MaxAge != 0 {
		cors.MaxAge = durationpb.New(c.MaxAge)
	}

	return policy(func(p *backplanev1.RoutePolicy) { p.Cors = cors })
}

// MaxRequestBytes caps the request body Envoy accepts; 0: the platform
// default.
func MaxRequestBytes(n uint64) PolicyOption {
	return policy(func(p *backplanev1.RoutePolicy) { p.MaxRequestBytes = n })
}
