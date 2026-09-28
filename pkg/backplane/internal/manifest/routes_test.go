package manifest_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

// A route matches by host and prefix together: the same prefix for two
// hosts is two routes, the same host and prefix twice is a duplicate.
func TestRouteMatchIsHostAndPrefix(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Route(&backplanev1.Route{Prefix: "/api/", Host: "a.example.com", Port: 80})
	b.Route(&backplanev1.Route{Prefix: "/api/", Host: "b.example.com", Port: 80})
	b.Route(&backplanev1.Route{Prefix: "/api/", Port: 80})

	if got := len(build(t, b).GetRoutes()); got != 3 {
		t.Fatalf("routes: %d", got)
	}

	b.Route(&backplanev1.Route{Prefix: "/api/", Host: "a.example.com", Port: 80})

	_, err := b.Build()
	if !errors.Is(err, manifest.ErrDuplicate) || !strings.Contains(err.Error(), "host a.example.com prefix /api/") {
		t.Fatalf("duplicate host+prefix: %v", err)
	}
}

func TestRoutePolicyChecked(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.Route(&backplanev1.Route{Prefix: "/ok/", Policy: &backplanev1.RoutePolicy{Timeout: durationpb.New(time.Minute)}})

	if got := build(t, b).GetRoutes()[0].GetPolicy().GetTimeout().AsDuration(); got != time.Minute {
		t.Fatalf("policy kept: %v", got)
	}

	b.Route(&backplanev1.Route{Prefix: "/bad/", Policy: &backplanev1.RoutePolicy{Retry: &backplanev1.RetryPolicy{}}})

	if _, err := b.Build(); !errors.Is(err, routes.ErrPolicy) || !strings.Contains(err.Error(), "/bad/") {
		t.Fatalf("bad policy: %v", err)
	}
}

func TestSDKVersion(t *testing.T) {
	t.Parallel()

	if manifest.SDKVersion() == "" {
		t.Fatal("empty SDK version")
	}
}
