package hook_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/hook"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

type (
	Quote struct {
		Amount int `json:"amount"`
	}
	Price struct {
		Total int `json:"total"`
	}
)

var errDeclined = errors.New("declined")

// bare is a service tree with no transport installed.
func bare(t *testing.T) (deps.Component, *env.Env) {
	t.Helper()

	e := env.New("svc", manifest.New("svc", "0.0.0"))
	app := node.New("svc", testlog.Discard(), e).Child("svc", node.Root, false)

	return link.Scope(app).(deps.Component), e
}

func TestDeclareRecordsHook(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := hook.Declare[Quote, Price](h.Root(), "price", hook.Required())
	hook.Declare[Quote, Price](h.Root(), "discount")

	if ref.Name() != "test.price" {
		t.Fatalf("name %q", ref.Name())
	}

	hooks := h.Manifest().GetHooks()
	if len(hooks) != 2 {
		t.Fatalf("hooks: %v", hooks)
	}

	price, discount := hooks[0], hooks[1]
	if price.GetName() != "price" || !price.GetRequired() || discount.GetName() != "discount" || discount.GetRequired() {
		t.Fatalf("hooks: %v", hooks)
	}

	if price.GetInput() == nil || price.GetOutput() == nil {
		t.Fatalf("schemas not reflected: %v", price)
	}
}

func TestDuplicateHookFailsManifest(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	hook.Declare[Quote, Price](root, "price")
	hook.Declare[Quote, Price](deps.NewComponent(root, "other"), "price")

	if _, err := e.Manifest.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestCallWithoutTransport(t *testing.T) {
	t.Parallel()

	root, _ := bare(t)
	ref := hook.Declare[Quote, Price](root, "price")

	if _, err := ref.Call(t.Context(), Quote{Amount: 1}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestCallAnswered(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := hook.Declare[Quote, Price](h.Root(), "price")
	h.Start()

	if _, err := ref.Call(t.Context(), Quote{Amount: 1}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("unanswered: %v", err)
	}

	backplanetest.Answer(h, ref, func(_ context.Context, in Quote) (Price, error) {
		if in.Amount < 0 {
			return Price{}, errDeclined
		}

		return Price{Total: in.Amount * 2}, nil
	})

	out, err := ref.Call(t.Context(), Quote{Amount: 21})
	if err != nil || out.Total != 42 {
		t.Fatalf("answered: %v %v", out, err)
	}

	if _, err := ref.Call(t.Context(), Quote{Amount: -1}); !errors.Is(err, errDeclined) ||
		!strings.Contains(err.Error(), "test.price") {
		t.Fatalf("declined: %v", err)
	}
}

func TestZeroRef(t *testing.T) {
	t.Parallel()

	var ref hook.Ref[Quote, Price]
	if ref.Name() != "" {
		t.Fatalf("name %q", ref.Name())
	}

	if _, err := ref.Call(t.Context(), Quote{}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("zero ref call: %v", err)
	}
}

func TestDeclareOnZeroScopePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "hook price declared on a zero scope") {
			t.Fatalf("panic %q", r)
		}
	}()

	hook.Declare[Quote, Price](deps.Component{}, "price")
}
