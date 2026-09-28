package route

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Origins is the browser origin policy of a WebSocket endpoint (ws-proto).
// It is a required argument where it applies; the
// zero value allows only same-origin browsers and non-browser clients.
type Origins struct {
	any      bool
	patterns []string
}

// AllowOrigins accepts upgrades from browsers on these host patterns
// ("app.example.com", "*.example.com") in addition to same-origin ones.
func AllowOrigins(patterns ...string) Origins { return Origins{patterns: patterns} }

// AnyOrigin accepts every origin: for endpoints that authenticate each call
// themselves and never rely on cookies.
func AnyOrigin() Origins { return Origins{any: true} }

// IsAny reports whether every origin is accepted.
func (o Origins) IsAny() bool { return o.any }

// Patterns are the accepted host patterns.
func (o Origins) Patterns() []string { return append([]string(nil), o.patterns...) }

// Allow reports whether r may upgrade. Requests without an Origin header
// (non-browser clients) and same-origin requests are always allowed.
func (o Origins) Allow(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if o.any || origin == "" {
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}

	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}

	for _, p := range o.patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(parsed.Host)); ok {
			return true
		}
	}

	return false
}
