package temporal

import "github.com/gopherex/backplane/pkg/backplane/internal/decl"

// CheckName validates a declaration name.
func CheckName(kind, name string) { decl.CheckName(kind, name) }
