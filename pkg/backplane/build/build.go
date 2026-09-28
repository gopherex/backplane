// Package build carries what the deployment stamps into the binary at link
// time. Set with -ldflags:
//
//	-X github.com/gopherex/backplane/pkg/backplane/build.Service=hello
//	-X github.com/gopherex/backplane/pkg/backplane/build.Version=1.2.0
//	-X github.com/gopherex/backplane/pkg/backplane/build.Commit=$(git rev-parse HEAD)
//	-X github.com/gopherex/backplane/pkg/backplane/build.Date=$(date -u +%FT%TZ)
//
// Version, Commit and Date fall back to debug.ReadBuildInfo; Service has no
// fallback and must be stamped (or given explicitly to the SDK).
package build

import (
	"errors"
	"runtime/debug"
)

// ErrUnnamed: build.Service was not stamped and no name was given.
var ErrUnnamed = errors.New("service name unknown: stamp build.Service with -ldflags -X")

const shortRevision = 12

// Set by the linker.
//
//nolint:gochecknoglobals // -ldflags -X can only set package variables
var (
	Service string
	Version string
	Commit  string
	Date    string
)

// Info is the resolved build information.
type Info struct {
	Service string
	Version string
	Commit  string
	Date    string
}

// Get returns the stamped values, filling gaps from the Go build info.
func Get() Info {
	info := Info{Service: Service, Version: Version, Commit: Commit, Date: Date}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}

	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = s.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		}
	}

	if info.Version == "" && info.Commit != "" {
		info.Version = "0.0.0+" + short(info.Commit)
	}

	return info
}

func short(rev string) string {
	if len(rev) > shortRevision {
		return rev[:shortRevision]
	}

	return rev
}
