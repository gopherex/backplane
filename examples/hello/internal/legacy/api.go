package legacy

import _ "embed"

// API describes the response served by this listener.
//
//go:embed openapi.yaml
var API []byte
