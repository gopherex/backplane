// Package routes holds what route options configure, for the SDK core.
package routes

import (
	"errors"
	"fmt"
	"strings"
)

// ErrPrefix: an HTTP path prefix the SDK cannot announce consistently.
var ErrPrefix = errors.New("route: bad path prefix")

// Managed is a route the SDK serves.
type Managed struct {
	Listen    string
	Host      string
	Transcode bool   // gRPC: Connect (gRPC-Web, REST-JSON) at Envoy
	OpenAPI   []byte // HTTP
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
