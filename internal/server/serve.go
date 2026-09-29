package server

import (
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane"
)

// Declare is what the server serves beyond its tree: probes now; managed
// routes (the console's `/`, `/ws`, `/auth`, `/plugins`), the internal API
// and listeners of the components later — declared here, before Run, from
// svc.State().
func Declare(svc *backplane.Service[State]) {
	svc.ReadinessProbe(probe.FromError(svc.State().Ready))
}
