package formatter

import _ "embed"

// API describes the process-local statistics served by HTTP.
//
//go:embed openapi.yaml
var API []byte
