package server

import (
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/gopherex/backplane/pkg/backplane"
)

// Declare is what the server serves beyond its tree, declared before Run
// from svc.State(): the readiness. The components' own listeners (xDS,
// the console) belong to their nodes.
func Declare(svc *backplane.Service[State]) {
	svc.ReadinessProbe(probe.FromError(svc.State().Ready))
}
