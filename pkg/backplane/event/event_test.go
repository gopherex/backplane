package event_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/event"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

type Greeted struct {
	Name string `json:"name"`
}

var errRejected = errors.New("rejected")

// bare is a service tree with no transport installed.
func bare(t *testing.T) (deps.Component, *env.Env) {
	t.Helper()

	e := env.New("svc", manifest.New("svc", "0.0.0"))
	app := node.New("svc", testlog.Discard(), e).Child("svc", node.Root, false)

	return link.Scope(app).(deps.Component), e
}

// keys records the ordering keys events were published with.
type keys struct {
	mu   sync.Mutex
	keys []string
}

func (k *keys) Publish(_ context.Context, _, key string, _ []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.keys = append(k.keys, key)

	return nil
}

func (*keys) Call(context.Context, string, []byte) ([]byte, error) { return nil, env.ErrUnavailable }

func TestDeclareRecordsEvent(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")

	if ref.Name() != "test.Greeted" {
		t.Fatalf("name %q", ref.Name())
	}

	events := h.Manifest().GetEvents()
	if len(events) != 1 || events[0].GetName() != "Greeted" || events[0].GetSchema() == nil {
		t.Fatalf("events: %v", events)
	}
}

func TestDuplicateEventFailsManifest(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	event.Declare[Greeted](root, "Greeted")
	event.Declare[Greeted](deps.NewComponent(root, "other"), "Greeted")

	if _, err := e.Manifest.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestPublishWithoutTransport(t *testing.T) {
	t.Parallel()

	root, _ := bare(t)
	ref := event.Declare[Greeted](root, "Greeted")

	if err := ref.Publish(t.Context(), Greeted{Name: "x"}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestPublishRecorded(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")
	h.Start()

	for _, name := range []string{"ann", "bob"} {
		if err := ref.Publish(t.Context(), Greeted{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	got := backplanetest.Events(h, ref)
	if !slices.Equal(got, []Greeted{{Name: "ann"}, {Name: "bob"}}) {
		t.Fatalf("events %v", got)
	}
}

func TestPublishKey(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	rec := &keys{}
	e.SetTransport(rec)

	ref := event.Declare[Greeted](root, "Greeted")
	if err := ref.Publish(t.Context(), Greeted{}, event.Key("user-1")); err != nil {
		t.Fatal(err)
	}

	if err := ref.Publish(t.Context(), Greeted{}); err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if !slices.Equal(rec.keys, []string{"user-1", ""}) {
		t.Fatalf("keys %q", rec.keys)
	}
}

func TestReact(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	mailer := deps.NewComponent(h.Root(), "mailer")

	got := make(chan Greeted, 1)

	event.React(mailer, "iam.UserRegistered", func(_ context.Context, v Greeted) error {
		if v.Name == "" {
			return errRejected
		}

		got <- v

		return nil
	})
	event.React(h.Root(), "iam.UserRegistered", func(context.Context, Greeted) error { return nil })

	subs := h.Manifest().GetSubscriptions()
	if len(subs) != 2 || subs[0].GetEvent() != "iam.UserRegistered" ||
		subs[0].GetConsumer() != "mailer:iam.UserRegistered" || subs[1].GetConsumer() != "iam.UserRegistered" {
		t.Fatalf("subscriptions: %v", subs)
	}

	h.Start()

	if err := backplanetest.React(t.Context(), h, "mailer", "iam.UserRegistered", Greeted{Name: "ann"}); err != nil {
		t.Fatal(err)
	}

	if v := <-got; v.Name != "ann" {
		t.Fatalf("delivered %v", v)
	}

	if err := backplanetest.React(t.Context(), h, "mailer", "iam.UserRegistered", Greeted{}); !errors.Is(err, errRejected) {
		t.Fatalf("handler error: %v", err)
	}

	if err := backplanetest.React(t.Context(), h, "", "iam.UserRegistered", Greeted{}); err != nil {
		t.Fatalf("root reactor: %v", err)
	}

	if err := backplanetest.React(t.Context(), h, "nobody", "iam.UserRegistered", Greeted{}); err == nil {
		t.Fatal("delivered to an undeclared reactor")
	}
}

func TestDuplicateReactorFailsManifest(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	noop := func(context.Context, Greeted) error { return nil }

	event.React(root, "iam.UserRegistered", noop)
	event.React(root, "iam.UserRegistered", noop)

	if _, err := e.Manifest.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestZeroRef(t *testing.T) {
	t.Parallel()

	var ref event.Ref[Greeted]
	if ref.Name() != "" {
		t.Fatalf("name %q", ref.Name())
	}

	if err := ref.Publish(t.Context(), Greeted{}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("zero ref publish: %v", err)
	}
}

func TestDeclareOnZeroScopePanics(t *testing.T) {
	t.Parallel()

	for what, declare := range map[string]func(){
		"event Greeted": func() { event.Declare[Greeted](deps.Component{}, "Greeted") },
		"reactor of iam.UserRegistered": func() {
			event.React(deps.Component{}, "iam.UserRegistered", func(context.Context, Greeted) error { return nil })
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, what+" declared on a zero scope") {
					t.Fatalf("panic %q", r)
				}
			}()

			declare()
		})
	}
}
