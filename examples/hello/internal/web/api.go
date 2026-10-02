package web

import "embed"

// API is carried in the binary and delivered on the authenticated platform port.
// The root document refers to files under schemas/ without a build-time bundler.
//
//go:embed openapi.yaml schemas
var API embed.FS
