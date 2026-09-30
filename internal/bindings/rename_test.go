package bindings_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/backplane/internal/bindings"
)

// A rename changes reads of the step and nothing else: not a string, not
// a field of the same name, not a comprehension variable shadowing it.
func TestRenameBinding(t *testing.T) {
	t.Parallel()

	b := mustBinding(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: "render.subject + ' render ' + req.render"
      text: "[1].map(render, render + 1).size() > 0 ? render.text : ''"
    when: "!steps.render.skipped && !steps.`+"`render`"+`.skipped"
    after: [render]
result: {message_id: "send.id + render . subject"}
editor: {nodes: {render: {x: 1, y: 2}}}
`)

	got, vs := bindings.RenameBinding(b, "render", "tmpl")
	if len(vs) > 0 {
		t.Fatal(vs)
	}

	want := mustBinding(t, `
hook: iam.SendEmail
steps:
  tmpl: {activity: template.Exec, input: {name: req.template}}
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: "tmpl.subject + ' render ' + req.render"
      text: "[1].map(render, render + 1).size() > 0 ? tmpl.text : ''"
    when: "!steps.tmpl.skipped && !steps.`+"`tmpl`"+`.skipped"
    after: [tmpl]
result: {message_id: "send.id + tmpl . subject"}
editor: {nodes: {tmpl: {x: 1, y: 2}}}
`)

	if !proto.Equal(got.PB(), want.PB()) {
		t.Fatalf("got\n%v\nwant\n%v", got.PB(), want.PB())
	}

	if _, onto := bindings.RenameBinding(b, "render", "send"); !has(onto, "/steps/render", bindings.CodeInvalidName) {
		t.Fatalf("rename onto another step: %v", onto)
	}

	if _, unknown := bindings.RenameBinding(b, "nope", "x"); !has(unknown, "/steps/nope", bindings.CodeUnknownStep) {
		t.Fatalf("rename of an unknown step: %v", unknown)
	}

	broken := mustBinding(t, "hook: iam.SendEmail\nsteps: {a: {activity: billing.Charge}}\nresult: \"a.\"")
	if _, unparsed := bindings.RenameBinding(broken, "a", "b"); !has(unparsed, "/result", bindings.CodeCEL) {
		t.Fatalf("rename through an expression that does not parse: %v", unparsed)
	}
}
