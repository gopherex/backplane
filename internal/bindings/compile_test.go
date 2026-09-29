package bindings_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/bindings"
)

// §7.1's example compiles into two groups with the options resolved from
// the binding, the manifests and the platform.
func TestCompileSendEmail(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustParse(t, sendEmail))

	if p.Kind() != bindings.KindBinding || p.Source() != "iam.SendEmail" {
		t.Fatalf("kind %v source %q", p.Kind(), p.Source())
	}

	if got := p.Groups(); !slices.EqualFunc(got, [][]string{{"render"}, {"send"}}, slices.Equal) {
		t.Fatalf("groups %v", got)
	}

	render, _ := p.Step("render")
	if render.Service != "template" || render.Kind != backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY ||
		render.StartToClose != 5*time.Second || render.Retry.Attempts != 2 || len(render.Deps) != 0 {
		t.Fatalf("render %+v", render)
	}

	send, _ := p.Step("send")
	want := bindings.Retry{
		Attempts: bindings.DefaultAttempts, InitialInterval: bindings.DefaultInitialInterval,
		MaxInterval: bindings.DefaultMaxInterval, Backoff: bindings.DefaultBackoff,
	}

	if send.Retry != want || send.StartToClose != bindings.DefaultStartToClose || send.Heartbeat != 3*time.Second ||
		!slices.Equal(send.Deps, []string{"render"}) || send.Group != 1 {
		t.Fatalf("send %+v", send)
	}
}

// Steps without dependencies share a group; after and references order
// them; the binding's options win over the manifest's.
func TestCompileGroups(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustParse(t, `iam.SendEmail :=
  a = template.Exec(name: "a")
  b = template.Exec(name: "b") [retry: 7, timeout: 2s, retry_backoff: 1.5]
  c = billing.Charge(amount: 1) [after: a]
  d = smtp.Send(to: req.to, subject: a.subject, text: b.text)
  e = billing.Charge({"x": steps.d.skipped})
  return { message_id: d.id }
`))

	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if got := p.Groups(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("groups %v, want %v", got, want)
	}

	b, _ := p.Step("b")
	if b.Retry.Attempts != 7 || b.StartToClose != 2*time.Second || b.Retry.Backoff != 1.5 {
		t.Fatalf("b %+v", b)
	}

	if d, _ := p.Step("d"); !slices.Equal(d.Deps, []string{"a", "b"}) {
		t.Fatalf("d deps %v", d.Deps)
	}

	if e, _ := p.Step("e"); !slices.Equal(e.Deps, []string{"d"}) {
		t.Fatalf("e deps %v", e.Deps)
	}
}

// Every violation names its place and its code.
func TestViolations(t *testing.T) {
	t.Parallel()

	const head = "iam.SendEmail :=\n"

	const (
		sendStep = "  send = smtp.Send(to: req.to, subject: \"s\", text: \"t\")\n"
		ok       = sendStep + "  return { message_id: send.id }\n"
	)

	cases := []struct {
		name, text, path, code string
	}{
		{"unknown hook", "iam.Nope :=\n" + ok, "hook", bindings.CodeUnknownHook},
		{"bad hook name", "IAM.send :=\n" + ok, "hook", bindings.CodeInvalidName},
		{"unknown activity", head + "  x = smtp.Nope()\n" + ok, "steps[0].activity", bindings.CodeUnknownActivity},
		{"reserved name", head + "  req = billing.Charge()\n" + ok, "steps[0].name", bindings.CodeReservedName},
		{
			"duplicate step", head + sendStep + "  send = billing.Charge()\n  return { message_id: \"x\" }\n",
			"steps[1].name", bindings.CodeDuplicateStep,
		},
		{"unknown after", head + "  x = billing.Charge() [after: nope]\n" + ok, "steps[0].after[0]", bindings.CodeUnknownStep},
		{"after itself", head + "  x = billing.Charge() [after: x]\n" + ok, "steps[0].after[0]", bindings.CodeCycle},
		{"reads itself", head + "  x = billing.Charge(v: x)\n" + ok, "steps[0]", bindings.CodeCycle},
		{"cycle", head + "  x = billing.Charge(v: y) \n  y = billing.Charge(v: x)\n" + ok, "steps[0]", bindings.CodeCycle},
		{"unknown variable", head + "  x = billing.Charge(v: nope)\n" + ok, "steps[0].input.v", bindings.CodeCEL},
		{"unknown field of req", head + "  x = billing.Charge(v: req.nope)\n" + ok, "steps[0].input.v", bindings.CodeCEL},
		{
			"unknown field of a step", head + sendStep + "  return { message_id: send.nope }\n",
			"result.message_id", bindings.CodeCEL,
		},
		{
			"type mismatch", head + "  send = smtp.Send(to: 1, subject: \"s\", text: \"t\")\n  return { message_id: send.id }\n",
			"steps[0].input.to", bindings.CodeTypeMismatch,
		},
		{"double into int", head + "  send = smtp.Send(to: \"a\", subject: \"s\", text: \"t\", priority: 1.5)\n" +
			"  return { message_id: send.id }\n", "steps[0].input.priority", bindings.CodeTypeMismatch},
		{"unknown input field", head + "  send = smtp.Send(to: \"a\", subject: \"s\", text: \"t\", cc: \"b\")\n" +
			"  return { message_id: send.id }\n", "steps[0].input.cc", bindings.CodeUnknownField},
		{
			"missing input field", head + "  send = smtp.Send(to: \"a\", subject: \"s\")\n  return { message_id: send.id }\n",
			"steps[0].input", bindings.CodeMissingField,
		},
		{"missing result field", head + sendStep, "result", bindings.CodeMissingField},
		{
			"result mismatch", head + sendStep + "  return { message_id: send.size > 1 }\n",
			"result.message_id", bindings.CodeTypeMismatch,
		},
		{"when not bool", head + "  x = billing.Charge() [when: req.to]\n" + ok, "steps[0].when", bindings.CodeTypeMismatch},
		{
			"undo input reads a later step", head + sendStep +
				"  y = smtp.Send(to: \"a\", subject: \"s\", text: send.id) [undo: smtp.Recall(id: z.id)]\n" +
				"  z = smtp.Send(to: \"a\", subject: \"s\", text: \"t\")\n  return { message_id: send.id }\n",
			"steps[1].undo_input", bindings.CodeUndoReference,
		},
		{"unknown undo", head + "  x = billing.Charge() [undo: billing.Nope]\n" + ok, "steps[0].undo", bindings.CodeUnknownActivity},
		{"bad backoff", head + "  x = billing.Charge() [retry_backoff: 0.5]\n" + ok, "steps[0].retry.backoff", bindings.CodeInvalidOption},
		{
			"intervals", head + "  x = billing.Charge() [retry_interval: 1m, retry_max_interval: 1s]\n" + ok,
			"steps[0].retry.max_interval", bindings.CodeInvalidOption,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			vs, err := bindings.ValidateBinding(mustParse(t, tc.text), manifests(t))
			if err != nil {
				t.Fatal(err)
			}

			if !has(vs, tc.path, tc.code) {
				t.Fatalf("want %s %s, got %v", tc.path, tc.code, vs)
			}
		})
	}
}

// A definition built directly (the console's form) is checked for what
// the text form cannot express.
func TestViolationsOfModel(t *testing.T) {
	t.Parallel()

	b := bindings.Binding{
		Hook: "iam.Audit",
		Steps: []bindings.Step{
			{Name: "1x", Activity: "billing.Charge"},
			{Name: "y", Activity: "billing.Charge", Input: bindings.Value{Expr: "{}", Fields: []bindings.Field{{Name: "a", Expr: "1"}}}},
			{Name: "z", Activity: "billing.Charge", Input: bindings.Value{Fields: []bindings.Field{
				{Name: "a", Expr: "1"}, {Name: "a", Expr: "2"},
			}}},
			{Name: "w", Activity: "billing.Charge", Retry: bindings.Retry{Attempts: -1}, UndoInput: bindings.Value{Expr: "w"}},
		},
	}

	vs, err := bindings.ValidateBinding(b, manifests(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range [][2]string{
		{"steps[0].name", bindings.CodeInvalidName},
		{"steps[1].input", bindings.CodeInvalidValue},
		{"steps[2].input.a", bindings.CodeDuplicateField},
		{"steps[3].retry.attempts", bindings.CodeInvalidOption},
		{"steps[3].undo_input", bindings.CodeInvalidOption},
	} {
		if !has(vs, want[0], want[1]) {
			t.Errorf("want %s %s, got %v", want[0], want[1], vs)
		}
	}

	_, err = bindings.CompileBinding(b, manifests(t))

	var invalid *bindings.InvalidError
	if !errors.As(err, &invalid) || len(invalid.Violations) != len(vs) {
		t.Fatalf("compile: %v", err)
	}
}

// A rule's when sees event and meta; its steps see event.
func TestRuleViolations(t *testing.T) {
	t.Parallel()

	cat := manifests(t)

	r, err := bindings.ParseRule(`on iam.UserRegistered when event.email != "" && meta.source != "" :=
  send = smtp.Send(to: event.email, subject: "welcome", text: event.name)
`)
	if err != nil {
		t.Fatal(err)
	}

	if vs, err := bindings.ValidateRule(r, cat); err != nil || len(vs) > 0 {
		t.Fatalf("valid rule: %v %v", vs, err)
	}

	for _, tc := range []struct{ text, path, code string }{
		{"on iam.Nope :=\n  x = billing.Charge()\n", "event", bindings.CodeUnknownEvent},
		{"on iam.UserRegistered when event.nope :=\n  x = billing.Charge()\n", "when", bindings.CodeCEL},
		{"on iam.UserRegistered when x.a == 1 :=\n  x = billing.Charge()\n", "when", bindings.CodeUnknownStep},
		{"on iam.UserRegistered :=\n  x = billing.Charge(v: req.a)\n", "steps[0].input.v", bindings.CodeCEL},
	} {
		bad, err := bindings.ParseRule(tc.text)
		if err != nil {
			t.Fatalf("%q: %v", tc.text, err)
		}

		vs, err := bindings.ValidateRule(bad, cat)
		if err != nil {
			t.Fatal(err)
		}

		if !has(vs, tc.path, tc.code) {
			t.Errorf("%q: want %s %s, got %v", tc.text, tc.path, tc.code, vs)
		}
	}
}

func has(vs []bindings.Violation, path, code string) bool {
	return slices.ContainsFunc(vs, func(v bindings.Violation) bool {
		return v.Path == path && v.Code == code && strings.TrimSpace(v.Message) != ""
	})
}
