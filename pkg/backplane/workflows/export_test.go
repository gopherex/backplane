package workflows

import (
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	internal "github.com/gopherex/backplane/pkg/backplane/internal/temporal"
)

// Declared returns the schedules scope's service declared and the error
// its manifest would fail Run with.
func Declared(scope deps.Scope) ([]internal.Schedule, error) {
	e, _ := decl.Env(scope, "test")

	var out []internal.Schedule

	for _, s := range e.Schedules() {
		if s, ok := s.(internal.Schedule); ok {
			out = append(out, s)
		}
	}

	_, err := e.Manifest.Build()

	return out, err
}

// Seal ends the declaration phase of scope's service.
func Seal(scope deps.Scope) {
	e, _ := decl.Env(scope, "test")
	e.Manifest.Seal()
}
