// hello is the reference service: every part of the SDK API in one place.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"google.golang.org/grpc"

	"github.com/gopherex/backplane/examples/hello/internal/greeter"
	"github.com/gopherex/backplane/examples/hello/internal/store"
	helloconsolev1 "github.com/gopherex/backplane/examples/hello/proto/hello/console/v1"
	helloui "github.com/gopherex/backplane/examples/hello/ui"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/activity"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/hook"
	"github.com/gopherex/backplane/pkg/backplane/route"
	"github.com/gopherex/backplane/pkg/backplane/wsproto"
)

// Config is the whole configuration: file < env (HELLO_*, BACKPLANE_* for the
// block) < Consul KV for Live fields only.
type Config struct {
	config.Backplane `json:"backplane"`

	Greeter greeter.Config `json:"greeter"`
	Store   store.Config   `json:"store"`
	Cache   store.Config   `json:"cache"`
}

// Payloads of the hook and the activity.
type (
	GreetIn struct {
		Name string `json:"name"`
	}
	GreetOut struct {
		Text string `json:"text"`
	}
	EchoIn struct {
		Text string `json:"text"`
	}
	EchoOut struct {
		Text string `json:"text"`
	}
)

// State is everything the service holds: a tree built under the Root.
type State struct {
	// Required: start waits for it (retrying), readiness follows its probe.
	Store deps.Dependency[*store.DB]
	// Optional: the service starts without it and keeps retrying.
	Cache deps.Optional[*store.DB]

	Greeter *greeter.Greeter
	Admin   *Admin
	Greet   hook.Ref[GreetIn, GreetOut]
}

// NewState wires the tree explicitly: every node gets what it needs by
// argument.
func NewState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()

	st := &State{
		Store: deps.NewDependency(root, store.New(&cfg.Store)),
		Cache: deps.NewOptional(root, store.New(&cfg.Cache), deps.Name("cache")),
		Greet: hook.Declare[GreetIn, GreetOut](root, "Greet", hook.Required()),
	}

	g, err := greeter.New(root, &cfg.Greeter, st.Store)
	if err != nil {
		return nil, fmt.Errorf("greeter: %w", err)
	}

	st.Greeter = g
	st.Admin = NewAdmin(root, g, st.Store, st.Cache)

	// What the service can do for bindings and rules.
	activity.Handle(root, "Echo", func(_ context.Context, in EchoIn) (EchoOut, error) { return EchoOut(in), nil })

	return st, nil
}

// Admin is the internal API for the console plugin.
type Admin struct {
	helloconsolev1.UnimplementedAdminServiceServer
	deps.Component

	greeter *greeter.Greeter
	db      deps.Dependency[*store.DB]
	cache   deps.Optional[*store.DB]
}

// NewAdmin creates the admin component.
func NewAdmin(
	parent deps.Scope, g *greeter.Greeter, db deps.Dependency[*store.DB], cache deps.Optional[*store.DB],
) *Admin {
	return &Admin{Component: deps.NewComponent(parent, "admin"), greeter: g, db: db, cache: cache}
}

// GetStats implements AdminService.
func (a *Admin) GetStats(
	ctx context.Context, _ *helloconsolev1.GetStatsRequest,
) (*helloconsolev1.GetStatsResponse, error) {
	text, err := a.greeter.Text(ctx, "you")
	if err != nil {
		return nil, err //nolint:wrapcheck // already carries the node path
	}

	// An optional dependency may be absent: Get says so.
	if cache, ok := a.cache.Get(); ok {
		cache.Inc(ctx, "stats")
	}

	return &helloconsolev1.GetStatsResponse{Greetings: a.db.Get().Total(), CurrentGreeting: text}, nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, NewState)
	if err != nil {
		return err //nolint:wrapcheck // prefixed by backplane
	}

	st := svc.State()

	// External API: one implementation over gRPC (+Connect) and ws-proto, plus HTTP.
	svc.GRPC(st.Greeter.Register, route.Transcode())
	// The zero origin policy: same-origin browsers and non-browser clients.
	wsproto.Serve(svc, "/ws/", route.Origins{}, st.Greeter.Register)
	svc.HTTP("/hello/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}

		// A bound hook answers first; the local greeter is the fallback.
		if out, err := st.Greet.Call(r.Context(), GreetIn{Name: name}); err == nil {
			fmt.Fprintln(w, out.Text)

			return
		}

		text, err := st.Greeter.Text(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		fmt.Fprintln(w, text) //nolint:gosec // text/plain, not HTML
	}))

	// Internal API and UI bundle for the console plugin.
	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, st.Admin) })

	bundle, err := fs.Sub(helloui.Dist, "dist")
	if err != nil {
		return errors.Join(fmt.Errorf("ui bundle: %w", err), svc.Close())
	}

	svc.UI(bundle)

	return svc.Run(ctx) //nolint:wrapcheck // prefixed by backplane
}
