// Package backplanetest runs components without Open, Run, ports or
// Consul: build them under Root, Start, and inspect what they published,
// answer their hooks, invoke their activities and reactors.
//
//	h := backplanetest.New(t)
//	g, _ := greeter.New(h.Root(), &cfg, deps.Static(db), greeted)
//	h.Start()
//	backplanetest.Events(h, greeted) // what g published
package backplanetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/event"
	"github.com/gopherex/backplane/pkg/backplane/hook"
	"github.com/gopherex/backplane/pkg/backplane/internal/decl"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

const stopBudget = 10 * time.Second

// ErrNotDeclared: no activity or reactor is declared under that name.
var ErrNotDeclared = errors.New("backplanetest: not declared")

// Harness is one service's tree, started by Start and stopped at cleanup.
type Harness struct {
	t   testing.TB
	svc *node.Node
	app *node.Node
	env *env.Env
	rec *recorder
}

// New creates a harness for a service named "test".
func New(tb testing.TB) *Harness {
	tb.Helper()

	m := manifest.New("test", "0.0.0")
	e := env.New("test", m)
	rec := &recorder{published: map[string][][]byte{}, hooks: map[string]env.Handler{}}
	e.SetBroker(rec)
	e.SetCaller(rec)

	svc := node.New("test", testlog.Discard(), e)

	return &Harness{t: tb, svc: svc, app: svc.Child("test", node.Root, false), env: e, rec: rec}
}

// Root is where components and dependencies under test are created.
func (h *Harness) Root() deps.Component {
	return link.Scope(h.app).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
}

// Start starts the tree (dependencies are provided, goroutines run) and
// stops it at test cleanup; a failure fails the test.
func (h *Harness) Start() {
	h.t.Helper()

	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopBudget)
		defer cancel()

		if err := h.svc.Stop(ctx); err != nil {
			h.t.Errorf("backplanetest: stop: %v", err)
		}
	})

	if err := h.svc.Start(h.t.Context()); err != nil {
		h.t.Fatalf("backplanetest: start: %v", err)
	}
}

// Manifest is what the components declared so far.
func (h *Harness) Manifest() *backplanev1.Manifest {
	h.t.Helper()

	m, err := h.env.Manifest.Build()
	if err != nil {
		h.t.Fatalf("backplanetest: manifest: %v", err)
	}

	return m
}

// Events decodes what was published to ref so far.
func Events[T any](h *Harness, ref event.Ref[T]) []T {
	h.t.Helper()

	raws := h.rec.events(ref.Name())
	out := make([]T, 0, len(raws))

	for _, raw := range raws {
		var v T
		if err := decl.Decode(raw, &v); err != nil {
			h.t.Fatalf("backplanetest: decode %s: %v", ref.Name(), err)
		}

		out = append(out, v)
	}

	return out
}

// Answer makes fn answer ref's calls; unanswered hooks fail with
// hook.ErrUnavailable as in production without a binding.
func Answer[Req, Res any](h *Harness, ref hook.Ref[Req, Res], fn func(ctx context.Context, in Req) (Res, error)) {
	h.rec.answer(ref.Name(), func(ctx context.Context, in []byte) ([]byte, error) {
		var req Req
		if err := decl.Decode(in, &req); err != nil {
			return nil, fmt.Errorf("backplanetest: decode %s: %w", ref.Name(), err)
		}

		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}

		return decl.Encode(res)
	})
}

// Activity invokes a declared activity as a binding would.
func Activity[Req, Res any](ctx context.Context, h *Harness, name string, in Req) (Res, error) {
	var out Res

	handler, ok := h.env.ActivityHandler(name)
	if !ok {
		return out, fmt.Errorf("%w: activity %q", ErrNotDeclared, name)
	}

	return invoke[Res](ctx, handler, in)
}

// React delivers v to the reactor declared for the event name (full,
// "iam.UserRegistered") on the scope at path ("" for the root), as the
// broker would: once, with the reactor's event.Timeout on ctx. The error
// is the handler's; nothing is redelivered or dead-lettered.
func React[T any](ctx context.Context, h *Harness, path, name string, v T) error {
	consumer := name
	if path != "" {
		consumer = path + ":" + name
	}

	r, ok := h.env.ReactorOf(consumer)
	if !ok {
		return fmt.Errorf("%w: reactor %q", ErrNotDeclared, consumer)
	}

	if r.Delivery.Timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, r.Delivery.Timeout)
		defer cancel()
	}

	_, err := invoke[struct{}](ctx, r.Handler, v)

	return err
}

func invoke[Res any](ctx context.Context, handler env.Handler, in any) (Res, error) {
	var out Res

	raw, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("backplanetest: encode: %w", err)
	}

	res, err := handler(ctx, raw)
	if err != nil || res == nil {
		return out, err
	}

	if err := decl.Decode(res, &out); err != nil {
		return out, fmt.Errorf("backplanetest: decode: %w", err)
	}

	return out, nil
}

// SetLive changes a Live field as the console would; watchers fire.
func SetLive[T any](l *config.Live[T], v T) { link.SetLive(l, v) }

type recorder struct {
	mu        sync.Mutex
	published map[string][][]byte
	hooks     map[string]env.Handler
}

func (r *recorder) Publish(_ context.Context, name, _ string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.published[name] = append(r.published[name], payload)

	return nil
}

func (r *recorder) Call(ctx context.Context, name string, in []byte) ([]byte, error) {
	r.mu.Lock()
	fn, ok := r.hooks[name]
	r.mu.Unlock()

	if !ok {
		return nil, hook.ErrUnavailable
	}

	return fn(ctx, in)
}

func (r *recorder) events(name string) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([][]byte(nil), r.published[name]...)
}

func (r *recorder) answer(name string, fn env.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks[name] = fn
}
