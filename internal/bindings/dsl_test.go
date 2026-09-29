package bindings_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gopherex/backplane/internal/bindings"
)

// §7.1's example is canonical text: Format of its parse is itself.
func TestFormatCanonical(t *testing.T) {
	t.Parallel()

	b := mustParse(t, sendEmail)

	want := bindings.Binding{
		Hook: "iam.SendEmail",
		Steps: []bindings.Step{
			{Name: "render", Activity: "template.Exec", Input: bindings.Value{Fields: []bindings.Field{
				{Name: "name", Expr: "req.template"}, {Name: "data", Expr: "req.data"},
			}}},
			{Name: "send", Activity: "smtp.Send", Input: bindings.Value{Fields: []bindings.Field{
				{Name: "to", Expr: "req.to"}, {Name: "subject", Expr: "render.subject"}, {Name: "text", Expr: "render.text"},
			}}},
		},
		Result: bindings.Value{Fields: []bindings.Field{{Name: "message_id", Expr: "send.id"}}},
	}

	if !b.Equal(want) {
		t.Fatalf("parse:\n%+v\nwant\n%+v", b, want)
	}

	if got := bindings.FormatBinding(b); got != sendEmail {
		t.Fatalf("format:\n%s\nwant\n%s", got, sendEmail)
	}
}

// Parse of Format is the definition, for every option and value form.
func TestFormatRoundTrip(t *testing.T) {
	t.Parallel()

	bindingDefs := []bindings.Binding{
		{Hook: "iam.Audit"},
		{
			Hook: "iam.SendEmail",
			Steps: []bindings.Step{
				{Name: "a", Activity: "billing.Charge"},
				{
					Name: "b", Activity: "billing.Charge", Input: bindings.Value{Expr: `{"k": [1, 2], "s": "x, y: z"}`},
					When: `a.ok && req.to.contains(",")`, After: []string{"a"},
					Undo: "billing.Refund", UndoInput: bindings.Value{Fields: []bindings.Field{{Name: "id", Expr: "b.id"}}},
					Retry: bindings.Retry{
						Attempts: 4, InitialInterval: 500 * time.Millisecond, MaxInterval: time.Minute, Backoff: 1.5,
					},
					StartToClose: 90 * time.Second, Heartbeat: 5 * time.Second,
				},
				{
					Name: "c_2", Activity: "smtp.Send", After: []string{"a", "b"}, Undo: "smtp.Recall",
					Input: bindings.Value{Fields: []bindings.Field{
						{Name: "message-id", Expr: `"//not a comment"`}, {Name: "n", Expr: "b.list.map(x, x * 2)"},
					}},
				},
			},
			Result: bindings.Value{Expr: `c_2.id + "!"`},
		},
		{
			Hook:   "iam.SendEmail",
			Steps:  []bindings.Step{{Name: "x", Activity: "billing.Charge", Input: bindings.Value{Expr: "req"}}},
			Result: bindings.Value{Fields: []bindings.Field{{Name: "a b", Expr: "x"}, {Name: "c", Expr: "{'d': 1}"}}},
		},
	}

	for _, b := range bindingDefs {
		text := bindings.FormatBinding(b)

		back, err := bindings.ParseBinding(text)
		if err != nil {
			t.Fatalf("parse of\n%s: %v", text, err)
		}

		if !back.Equal(b) {
			t.Fatalf("round trip of\n%s\ngot  %+v\nwant %+v", text, back, b)
		}
	}

	ruleDefs := []bindings.Rule{
		{Event: "iam.UserRegistered", Steps: []bindings.Step{{Name: "a", Activity: "billing.Charge"}}},
		{
			Event: "iam.UserRegistered", When: `event.email != "" && meta.source == "iam"`,
			Steps: []bindings.Step{{
				Name: "s", Activity: "smtp.Send", When: "true",
				Input: bindings.Value{Fields: []bindings.Field{{Name: "to", Expr: "event.email"}}},
			}},
		},
	}

	for _, r := range ruleDefs {
		text := bindings.FormatRule(r)

		back, err := bindings.ParseRule(text)
		if err != nil {
			t.Fatalf("parse of\n%s: %v", text, err)
		}

		if !back.Equal(r) {
			t.Fatalf("round trip of\n%s\ngot  %+v\nwant %+v", text, back, r)
		}
	}
}

// Statements span lines inside brackets; comments and blank lines go;
// lists end with a comma.
func TestParseMultiline(t *testing.T) {
	t.Parallel()

	b := mustParse(t, `// greeting
iam.SendEmail :=

  render = template.Exec(  // the template
    name: req.template,
    data: req.data,
  )
  send = smtp.Send(to: req.to, subject: render.subject, text: "http://x") [
    retry: 5, timeout: 10s, after: [render],
  ]
  return {
    "message_id": send.id,
  }
`)

	want := bindings.Binding{
		Hook: "iam.SendEmail",
		Steps: []bindings.Step{
			{Name: "render", Activity: "template.Exec", Input: bindings.Value{Fields: []bindings.Field{
				{Name: "name", Expr: "req.template"}, {Name: "data", Expr: "req.data"},
			}}},
			{
				Name: "send", Activity: "smtp.Send", After: []string{"render"},
				Retry: bindings.Retry{Attempts: 5}, StartToClose: 10 * time.Second,
				Input: bindings.Value{Fields: []bindings.Field{
					{Name: "to", Expr: "req.to"}, {Name: "subject", Expr: "render.subject"}, {Name: "text", Expr: `"http://x"`},
				}},
			},
		},
		Result: bindings.Value{Fields: []bindings.Field{{Name: "message_id", Expr: "send.id"}}},
	}

	if !b.Equal(want) {
		t.Fatalf("got\n%+v\nwant\n%+v", b, want)
	}

	// On one line with the header too.
	r, err := bindings.ParseRule(`on iam.UserRegistered := a = billing.Charge()`)
	if err != nil || r.Event != "iam.UserRegistered" || len(r.Steps) != 1 {
		t.Fatalf("one line: %+v %v", r, err)
	}
}

// Errors say where: line and column (characters) of the text.
func TestParseErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, text string
		rule       bool
		line, col  int
		msg        string
	}{
		{"empty", "  \n// only a comment\n", false, 1, 1, "empty definition"},
		{"no header", "iam.SendEmail\n  x = a.B()\n", false, 1, 1, `":="`},
		{"unknown option", "iam.SendEmail :=\n  send = smtp.Send(to: req.to) [retries: 3]\n", false, 2, 33, "unknown option"},
		{"unclosed call", "iam.SendEmail :=\n  send = smtp.Send(to: req.to\n", false, 2, 19, "unclosed ("},
		{"not a step", "iam.SendEmail :=\n  smtp.Send()\n", false, 2, 3, "expected a step"},
		{"step after return", "iam.SendEmail :=\n  return {}\n  x = a.B()\n", false, 3, 3, "after return"},
		{"bad duration", "iam.SendEmail :=\n  x = a.B() [timeout: 10]\n", false, 2, 23, "duration"},
		{"bad field", "iam.SendEmail :=\n  x = a.B(to: 1, 2)\n", false, 2, 18, "<field>: <cel>"},
		{"rule return", "on iam.UserRegistered :=\n  return {}\n", true, 2, 3, "no return"},
		{"rule without on", "iam.UserRegistered :=\n  x = a.B()\n", true, 1, 1, `"on`},
		{"binding with on", "on iam.UserRegistered :=\n  x = a.B()\n", false, 1, 1, "remove"},
	}

	for _, tc := range cases {
		var err error
		if tc.rule {
			_, err = bindings.ParseRule(tc.text)
		} else {
			_, err = bindings.ParseBinding(tc.text)
		}

		var pes bindings.ParseErrors
		if !errors.As(err, &pes) || len(pes) == 0 {
			t.Errorf("%s: want parse errors, got %v", tc.name, err)

			continue
		}

		if pes[0].Line != tc.line || pes[0].Column != tc.col || !strings.Contains(pes[0].Message, tc.msg) {
			t.Errorf("%s: got %v, want %d:%d %q", tc.name, pes[0], tc.line, tc.col, tc.msg)
		}
	}
}

// A CEL syntax error points into the expression, in the text's lines.
func TestParseCELErrorPosition(t *testing.T) {
	t.Parallel()

	_, err := bindings.ParseBinding("iam.SendEmail :=\n  ok = a.B(v: 1)\n  send = smtp.Send(to: req.to +)\n")

	var pes bindings.ParseErrors
	if !errors.As(err, &pes) {
		t.Fatalf("want parse errors, got %v", err)
	}

	// "req.to +" starts at column 24 of line 3.
	if e := pes[0]; e.Line != 3 || e.Column < 24 || e.Column > 32 || !strings.HasPrefix(e.Message, "cel: ") {
		t.Fatalf("got %v", e)
	}
}
