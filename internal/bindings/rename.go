package bindings

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
)

// RenameBinding is b with step from renamed to: its key, every `after`
// naming it, its editor position and every expression reading it (by the
// expressions' syntax trees: a string or a field of the same name stays).
// Refused: the violations say why.
func RenameBinding(b Binding, from, to string) (Binding, []Violation) {
	r := renamer{from: from, to: to}
	if vs := r.check(b.Steps); len(vs) > 0 {
		return Binding{}, vs
	}

	out := b
	out.Steps = r.steps(b.Steps)
	out.Result = r.value(pathResult, b.Result)
	out.Editor = r.editor(b.Editor)

	if len(r.vs) > 0 {
		return Binding{}, r.vs
	}

	return out, nil
}

// RenameRule is RenameBinding for a rule.
func RenameRule(rule Rule, from, to string) (Rule, []Violation) {
	r := renamer{from: from, to: to}
	if vs := r.check(rule.Steps); len(vs) > 0 {
		return Rule{}, vs
	}

	out := rule
	out.Steps = r.steps(rule.Steps)
	out.When = r.expr(pathWhen, rule.When)
	out.Editor = r.editor(rule.Editor)

	if len(r.vs) > 0 {
		return Rule{}, r.vs
	}

	return out, nil
}

type renamer struct {
	from, to string
	vs       []Violation
}

func (r *renamer) violate(path, code, msg string) {
	r.vs = append(r.vs, Violation{Path: path, Code: code, Message: msg})
}

// check refuses a rename of a step that does not exist or to a name that
// is not free.
func (r *renamer) check(steps []Step) []Violation {
	path := Pointer("steps", r.from)
	exists := slices.ContainsFunc(steps, func(s Step) bool { return s.Name == r.from })

	switch {
	case !exists:
		r.violate(path, CodeUnknownStep, "no step "+r.from)
	case !IsIdent(r.to):
		r.violate(path, CodeInvalidName, fmt.Sprintf("%q is not an identifier [A-Za-z_][A-Za-z0-9_]*", r.to))
	case reserved[r.to]:
		r.violate(path, CodeReservedName, fmt.Sprintf("%q is reserved", r.to))
	case r.to != r.from && slices.ContainsFunc(steps, func(s Step) bool { return s.Name == r.to }):
		r.violate(path, CodeInvalidName, "step "+r.to+" exists already")
	}

	return r.vs
}

func (r *renamer) steps(steps []Step) []Step {
	out := make([]Step, 0, len(steps))

	for i := range steps {
		s := steps[i]
		path := Pointer("steps", s.Name)

		s.When = r.expr(path+"/when", s.When)
		s.Input = r.value(path+"/input", s.Input)
		s.UndoInput = r.value(path+"/undoInput", s.UndoInput)
		s.After = slices.Clone(s.After)

		for i, a := range s.After {
			if a == r.from {
				s.After[i] = r.to
			}
		}

		if s.Name == r.from {
			s.Name = r.to
		}

		out = append(out, s)
	}

	return sortSteps(out)
}

func (r *renamer) value(path string, v Value) Value {
	return v.mapExprs(path, r.expr)
}

// expr is src with every read of the step renamed.
func (r *renamer) expr(path, src string) string {
	if src == "" {
		return src
	}

	env, err := baseEnv()
	if err != nil {
		r.violate(path, CodeCEL, err.Error())

		return src
	}

	parsed, iss := env.Parse(src)
	if iss.Err() != nil {
		r.violate(path, CodeCEL, "the expression does not parse; fix it before renaming")

		return src
	}

	var spans []Range

	for _, rd := range readsOf(src, parsed.NativeRep()) {
		switch {
		case rd.variable == r.from:
			spans = append(spans, rd.ident)
		case rd.variable == VarSteps && len(rd.fields) > 0 && rd.fields[0] == r.from:
			spans = append(spans, rd.fieldAt[0])
		default:
			continue
		}

		if spans[len(spans)-1].IsZero() {
			r.violate(path, CodeCEL, "cannot locate "+r.from+" in the expression")

			return src
		}
	}

	runes := []rune(src)

	for i := len(spans) - 1; i >= 0; i-- {
		name := []rune(r.to)
		if string(runes[spans[i].Start:spans[i].End]) == "`"+r.from+"`" {
			name = []rune("`" + r.to + "`")
		}

		runes = append(runes[:spans[i].Start], append(name, runes[spans[i].End:]...)...)
	}

	return string(runes)
}

func (r *renamer) editor(e *consolev1.EditorLayout) *consolev1.EditorLayout {
	if e == nil {
		return nil
	}

	out, _ := proto.Clone(e).(*consolev1.EditorLayout)

	nodes := out.GetNodes()
	if at, ok := nodes[r.from]; ok {
		delete(nodes, r.from)
		nodes[r.to] = at
	}

	return out
}
