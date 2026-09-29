package bindings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// Platform defaults of a step, below the binding's own options and the
// activity's manifest defaults.
const (
	DefaultStartToClose    = 30 * time.Second
	DefaultAttempts        = 3
	DefaultInitialInterval = time.Second
	DefaultMaxInterval     = 30 * time.Second
	DefaultBackoff         = 2.0
)

// InvalidError is a definition that does not compile: the violations say
// why.
type InvalidError struct{ Violations []Violation }

func (e *InvalidError) Error() string {
	if len(e.Violations) == 0 {
		return "bindings: invalid definition"
	}

	msg := "bindings: invalid definition: " + e.Violations[0].String()
	if n := len(e.Violations) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}

	return msg
}

// ValidateBinding checks b against the catalog: every violation, empty
// when valid. The error is a failure to validate, not a violation.
func ValidateBinding(b Binding, cat Catalog) ([]Violation, error) {
	_, vs, err := compileBinding(b, cat)

	return vs, err
}

// ValidateRule checks r against the catalog, as ValidateBinding.
func ValidateRule(r Rule, cat Catalog) ([]Violation, error) {
	_, vs, err := compileRule(r, cat)

	return vs, err
}

// CompileBinding checks b against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileBinding(b Binding, cat Catalog) (*Program, error) {
	p, vs, err := compileBinding(b, cat)
	if err != nil {
		return nil, err
	}

	if len(vs) > 0 {
		return nil, &InvalidError{Violations: vs}
	}

	return p, nil
}

// CompileRule checks r against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileRule(r Rule, cat Catalog) (*Program, error) {
	p, vs, err := compileRule(r, cat)
	if err != nil {
		return nil, err
	}

	if len(vs) > 0 {
		return nil, &InvalidError{Violations: vs}
	}

	return p, nil
}

func compileBinding(b Binding, cat Catalog) (*Program, []Violation, error) {
	c := newCompiler(KindBinding, cat)

	var in, out *sp.Schema

	switch hook, ok := cat.Hook(b.Hook); {
	case !IsFullName(b.Hook):
		c.violate("hook", CodeInvalidName, fmt.Sprintf("%q is not a hook name <service>.<Hook>", b.Hook))
	case !ok:
		c.violate("hook", CodeUnknownHook, "no manifest declares hook "+b.Hook)
	default:
		in, out = hook.GetInput(), hook.GetOutput()
	}

	c.input = c.shapes.of(in, "bp.req")
	resultShape := c.shapes.of(out, "bp.result")

	return c.compile(spec{Kind: KindBinding, Source: b.Hook, Result: b.Result}, b.Steps, in, resultShape)
}

func compileRule(r Rule, cat Catalog) (*Program, []Violation, error) {
	c := newCompiler(KindRule, cat)

	var in *sp.Schema

	switch event, ok := cat.Event(r.Event); {
	case !IsFullName(r.Event):
		c.violate("event", CodeInvalidName, fmt.Sprintf("%q is not an event name <service>.<Event>", r.Event))
	case !ok:
		c.violate("event", CodeUnknownEvent, "no manifest declares event "+r.Event)
	default:
		in = event.GetSchema()
	}

	c.input = c.shapes.of(in, "bp.event")

	return c.compile(spec{Kind: KindRule, Source: r.Event, When: r.When}, r.Steps, in, nil)
}

type compiler struct {
	kind   Kind
	cat    Catalog
	shapes *shapes
	input  *shape
	vs     []Violation
	env    *cel.Env
}

func newCompiler(kind Kind, cat Catalog) *compiler {
	return &compiler{kind: kind, cat: cat, shapes: newShapes()}
}

func (c *compiler) violate(path, code, msg string) {
	c.vs = append(c.vs, Violation{Path: path, Code: code, Message: msg})
}

// draft is a step while it compiles.
type draft struct {
	Step

	path     string
	ok       bool // name valid and unique: a CEL variable
	act      *backplanev1.Activity
	undo     *backplanev1.Activity
	out      *sp.Schema
	outShape *shape
	deps     []string
	undoRefs []string
}

func (c *compiler) compile(s spec, steps []Step, in *sp.Schema, result *shape) (*Program, []Violation, error) {
	drafts := c.steps(steps)

	env, err := c.typedEnv(drafts)
	if err != nil {
		return nil, nil, err
	}

	c.env = env
	names := map[string]bool{}

	for _, d := range drafts {
		if d.ok {
			names[d.Name] = true
		}
	}

	for _, d := range drafts {
		c.expressions(d, names)
	}

	if s.Kind == KindRule && s.When != "" {
		if t, rs, ok := c.expr("when", s.When); ok {
			c.boolean("when", t)

			for _, r := range rs {
				if names[strings.TrimPrefix(r, VarSteps+".")] || r == VarSteps {
					c.violate("when", CodeUnknownStep, "a rule's when sees only event and meta, not steps")

					break
				}
			}
		}
	}

	if s.Kind == KindBinding {
		c.value("result", s.Result, result)
	}

	groups := c.order(drafts)
	c.undoRefs(drafts)

	if len(c.vs) > 0 {
		return nil, c.vs, nil
	}

	s.Input = schemaBytes(in)

	for _, d := range drafts {
		s.Steps = append(s.Steps, specStep{
			StepPlan: c.plan(d, groups), When: d.When, Input: d.Input, UndoInput: d.UndoInput,
			Output: schemaBytes(d.out),
		})
	}

	p, err := newProgram(s)
	if err != nil {
		return nil, nil, err
	}

	return p, nil, nil
}

// steps checks what does not need CEL: names, activities, options.
func (c *compiler) steps(steps []Step) []*draft {
	seen := map[string]bool{}
	out := make([]*draft, 0, len(steps))

	for i := range steps {
		s := &steps[i]
		d := &draft{Step: *s, path: fmt.Sprintf("steps[%d]", i)}
		out = append(out, d)

		switch {
		case !IsIdent(s.Name):
			c.violate(d.path+".name", CodeInvalidName, fmt.Sprintf("%q is not an identifier [A-Za-z_][A-Za-z0-9_]*", s.Name))
		case reserved[s.Name]:
			c.violate(d.path+".name", CodeReservedName, fmt.Sprintf("%q is reserved", s.Name))
		case seen[s.Name]:
			c.violate(d.path+".name", CodeDuplicateStep, fmt.Sprintf("step %s is declared twice", s.Name))
		default:
			d.ok = true
		}

		seen[s.Name] = true
		d.act = c.activity(d.path+".activity", s.Activity)

		if d.act != nil {
			d.out = d.act.GetOutput()
		}

		if s.Undo != "" {
			d.undo = c.activity(d.path+".undo", s.Undo)
		} else if !s.UndoInput.IsZero() {
			c.violate(d.path+".undo_input", CodeInvalidOption, "undo input without an undo activity")
		}

		c.options(d)
	}

	return out
}

func (c *compiler) activity(path, name string) *backplanev1.Activity {
	if !IsFullName(name) {
		c.violate(path, CodeInvalidName, fmt.Sprintf("%q is not an activity name <service>.<Activity>", name))

		return nil
	}

	a, ok := c.cat.Activity(name)
	if !ok {
		c.violate(path, CodeUnknownActivity, "no manifest declares activity "+name)

		return nil
	}

	return a
}

func (c *compiler) options(d *draft) {
	r := d.Retry

	if r.Attempts < 0 {
		c.violate(d.path+".retry.attempts", CodeInvalidOption, "attempts must not be negative")
	}

	if r.InitialInterval < 0 {
		c.violate(d.path+".retry.initial_interval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval < 0 {
		c.violate(d.path+".retry.max_interval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval > 0 && r.InitialInterval > r.MaxInterval {
		c.violate(d.path+".retry.max_interval", CodeInvalidOption, "must not be below the initial interval")
	}

	if r.Backoff != 0 && r.Backoff < 1 {
		c.violate(d.path+".retry.backoff", CodeInvalidOption, "must be at least 1")
	}

	if d.StartToClose < 0 {
		c.violate(d.path+".start_to_close", CodeInvalidOption, "must not be negative")
	}

	if d.Heartbeat < 0 {
		c.violate(d.path+".heartbeat", CodeInvalidOption, "must not be negative")
	}
}

// typedEnv declares the source, `steps` and every step by the shapes of
// the schemas.
func (c *compiler) typedEnv(drafts []*draft) (*cel.Env, error) {
	vars := map[string]*types.Type{}
	state := &shape{kind: kindObject, name: "bp.step_state", fields: map[string]*shape{"skipped": {kind: kindBool}}}
	stepsShape := &shape{kind: kindObject, name: "bp.steps", fields: map[string]*shape{}}
	c.shapes.objects[state.name] = state
	c.shapes.objects[stepsShape.name] = stepsShape

	if c.kind == KindBinding {
		vars[VarReq] = c.input.celType()
	} else {
		str := &shape{kind: kindString}
		meta := &shape{kind: kindObject, name: "bp.meta", fields: map[string]*shape{
			"id": str, "source": str, "subject": str, "type": str, "time": str,
		}}
		c.shapes.objects[meta.name] = meta
		vars[VarEvent] = c.input.celType()
		vars[VarMeta] = meta.celType()
	}

	for _, d := range drafts {
		if !d.ok {
			continue
		}

		d.outShape = c.shapes.of(d.out, "bp.step."+d.Name)
		vars[d.Name] = d.outShape.celType()
		stepsShape.fields[d.Name] = state
	}

	vars[VarSteps] = stepsShape.celType()

	return typedEnv(vars, c.shapes.objects)
}

// expressions compiles and type-checks the step's expressions and derives
// its dependencies.
func (c *compiler) expressions(d *draft, names map[string]bool) {
	var reads []string

	if d.When != "" {
		if t, rs, ok := c.expr(d.path+".when", d.When); ok {
			c.boolean(d.path+".when", t)

			reads = append(reads, rs...)
		}
	}

	var in *shape
	if d.act != nil {
		in = c.shapes.of(d.act.GetInput(), "bp.in."+d.Name)
	}

	reads = append(reads, c.value(d.path+".input", d.Input, in)...)

	if d.Undo != "" && !d.UndoInput.IsZero() {
		var undoIn *shape
		if d.undo != nil {
			undoIn = c.shapes.of(d.undo.GetInput(), "bp.undo."+d.Name)
		}

		d.undoRefs = stepRefs(c.value(d.path+".undo_input", d.UndoInput, undoIn), names)
	}

	for i, a := range d.After {
		switch {
		case a == d.Name:
			c.violate(fmt.Sprintf("%s.after[%d]", d.path, i), CodeCycle, fmt.Sprintf("step %s runs after itself", a))
		case !names[a]:
			c.violate(fmt.Sprintf("%s.after[%d]", d.path, i), CodeUnknownStep, "no step "+a)
		default:
			d.deps = appendNew(d.deps, a)
		}
	}

	for _, r := range stepRefs(reads, names) {
		if r == d.Name {
			c.violate(d.path, CodeCycle, fmt.Sprintf("step %s reads its own output", r))

			continue
		}

		d.deps = appendNew(d.deps, r)
	}
}

// stepRefs are the step names among what expressions read.
func stepRefs(reads []string, names map[string]bool) []string {
	var out []string

	for _, r := range reads {
		name := strings.TrimPrefix(r, VarSteps+".")
		if names[name] {
			out = appendNew(out, name)
		}
	}

	return out
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}

	return append(list, s)
}

// value compiles a Value and checks it against the shape it must have
// (nil: unknown, no check). It returns what the expressions read.
func (c *compiler) value(path string, v Value, want *shape) []string {
	if v.Expr != "" && len(v.Fields) > 0 {
		c.violate(path, CodeInvalidValue, "either fields or an expression, not both")

		return nil
	}

	if v.Expr != "" {
		return c.exprValue(path, v.Expr, want)
	}

	reads, seen := c.fields(path, v.Fields, want)

	if want != nil && want.kind == kindObject {
		for _, r := range want.required {
			if !seen[r] {
				c.violate(path, CodeMissingField, fmt.Sprintf("required field %s is not set", r))
			}
		}
	}

	if want != nil && want.kind != kindObject && want.kind != kindMap && want.kind != kindDyn {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got an object", want))
	}

	return reads
}

// exprValue compiles a Value given as one expression and checks its type
// against want (nil: no check). It returns what the expression reads.
func (c *compiler) exprValue(path, src string, want *shape) []string {
	t, reads, ok := c.expr(path, src)
	if ok && want != nil {
		if msg := c.assignable(want, t); msg != "" {
			c.violate(path, CodeTypeMismatch, msg)
		}
	}

	return reads
}

// fields compiles the fields of a Value given field by field, each
// against its shape in want. It returns what the expressions read and
// the names set.
func (c *compiler) fields(path string, fields []Field, want *shape) ([]string, map[string]bool) {
	var reads []string

	seen := map[string]bool{}

	for i, f := range fields {
		fpath := path + "." + f.Name
		if f.Name == "" {
			c.violate(fmt.Sprintf("%s.fields[%d]", path, i), CodeInvalidName, "field without a name")

			continue
		}

		if seen[f.Name] {
			c.violate(fpath, CodeDuplicateField, fmt.Sprintf("field %s is set twice", f.Name))

			continue
		}

		seen[f.Name] = true

		t, fieldReads, ok := c.expr(fpath, f.Expr)
		reads = append(reads, fieldReads...)

		if ok {
			c.field(fpath, f.Name, t, want)
		}
	}

	return reads, seen
}

func (c *compiler) field(path, name string, t *types.Type, want *shape) {
	var fieldShape *shape

	switch {
	case want == nil:
		return
	case want.kind == kindObject:
		fieldShape = want.fields[name]
		if fieldShape == nil {
			c.violate(path, CodeUnknownField, "the schema declares no field "+name)

			return
		}
	case want.kind == kindMap:
		fieldShape = want.elem
	default:
		return
	}

	if msg := c.assignable(fieldShape, t); msg != "" {
		c.violate(path, CodeTypeMismatch, msg)
	}
}

// expr compiles src in the typed environment: its output type and what it
// reads; a failure is a violation.
func (c *compiler) expr(path, src string) (*types.Type, []string, bool) {
	if strings.TrimSpace(src) == "" {
		c.violate(path, CodeCEL, "empty expression")

		return nil, nil, false
	}

	checked, iss := c.env.Compile(src)
	if iss.Err() != nil {
		c.violate(path, CodeCEL, iss.Err().Error())

		return nil, nil, false
	}

	return checked.OutputType(), refs(checked.NativeRep().Expr()), true
}

func (c *compiler) boolean(path string, t *types.Type) {
	switch t.Kind() {
	case types.BoolKind, types.DynKind, types.AnyKind, types.TypeParamKind:
	default:
		c.violate(path, CodeTypeMismatch, "expected bool, got "+t.String())
	}
}

// assignable is "" when a value of type t fits the shape as JSON: numbers
// widen (an int fits a double field, not the reverse), a map fits an
// object, timestamps and durations fit strings (their JSON form); dyn fits
// anything. Otherwise the message says why.
//
//nolint:cyclop // one case per kind
func (c *compiler) assignable(want *shape, t *types.Type) string {
	got := t.Kind()

	switch got {
	case types.DynKind, types.AnyKind, types.TypeParamKind, types.ErrorKind, types.NullTypeKind:
		return ""
	case types.OpaqueKind:
		if t.TypeName() == "optional_type" && len(t.Parameters()) == 1 {
			return c.assignable(want, t.Parameters()[0])
		}
	default:
	}

	mismatch := fmt.Sprintf("expected %s, got %s", want, t)

	switch want.kind {
	case kindDyn:
		return ""
	case kindInt, kindUint:
		if got == types.IntKind || got == types.UintKind {
			return ""
		}
	case kindDouble:
		if got == types.IntKind || got == types.UintKind || got == types.DoubleKind {
			return ""
		}
	case kindBool:
		if got == types.BoolKind {
			return ""
		}
	case kindString:
		if got == types.StringKind || got == types.TimestampKind || got == types.DurationKind {
			return ""
		}
	case kindBytes:
		if got == types.BytesKind {
			return ""
		}
	case kindList:
		if got == types.ListKind {
			return c.assignable(want.elem, t.Parameters()[0])
		}
	case kindMap:
		if got == types.MapKind {
			return c.assignable(want.elem, t.Parameters()[1])
		}

		if got == types.StructKind {
			return ""
		}
	case kindObject:
		return c.object(want, t, mismatch)
	}

	return mismatch
}

// object checks a value against an object shape field by field when both
// are known objects.
func (c *compiler) object(want *shape, t *types.Type, mismatch string) string {
	switch t.Kind() {
	case types.MapKind:
		return ""
	case types.StructKind:
		have := c.shapes.objects[t.TypeName()]
		if have == nil || have == want {
			return ""
		}

		for _, name := range sortedKeys(have.fields) {
			if f := want.fields[name]; f != nil {
				if msg := c.assignable(f, have.fields[name].celType()); msg != "" {
					return name + ": " + msg
				}
			}
		}

		return ""
	default:
		return mismatch
	}
}

// order derives the groups from the dependencies (Kahn's algorithm by
// levels, declaration order within a level) and reports cycles.
func (c *compiler) order(drafts []*draft) [][]string {
	index := map[string]int{}

	for i, d := range drafts {
		if d.ok {
			index[d.Name] = i
		}
	}

	level := map[string]int{}

	var groups [][]string

	for len(level) < len(index) {
		var ready []string

		for _, d := range drafts {
			if _, done := level[d.Name]; done || !d.ok {
				continue
			}

			if allIn(d.deps, level) {
				ready = append(ready, d.Name)
			}
		}

		if len(ready) == 0 {
			c.cycle(drafts, level)

			return nil
		}

		for _, name := range ready {
			level[name] = len(groups)
		}

		groups = append(groups, ready)
	}

	return groups
}

func allIn(deps []string, level map[string]int) bool {
	for _, dep := range deps {
		if _, ok := level[dep]; !ok {
			return false
		}
	}

	return true
}

// cycle reports one cycle among the steps not ordered.
func (c *compiler) cycle(drafts []*draft, level map[string]int) {
	byName := map[string]*draft{}

	for _, d := range drafts {
		if _, done := level[d.Name]; !done && d.ok {
			byName[d.Name] = d
		}
	}

	// Every step left has a dependency left: walking them must return.
	var start *draft

	for _, d := range drafts {
		if byName[d.Name] == d {
			start = d

			break
		}
	}

	var path []string

	seen := map[string]int{}

	for cur := start; cur != nil; {
		if at, ok := seen[cur.Name]; ok {
			loop := append(path[at:], cur.Name) //nolint:gocritic // a new slice for the message
			c.violate(byName[loop[0]].path, CodeCycle, "cycle: "+strings.Join(loop, " -> "))

			return
		}

		seen[cur.Name] = len(path)
		path = append(path, cur.Name)

		var next *draft

		for _, dep := range cur.deps {
			if byName[dep] != nil {
				next = byName[dep]

				break
			}
		}

		cur = next
	}
}

// undoRefs checks that an undo input reads only the step itself and what
// ran before it: nothing else is certain to have run when it compensates.
func (c *compiler) undoRefs(drafts []*draft) {
	byName := map[string]*draft{}

	for _, d := range drafts {
		if d.ok {
			byName[d.Name] = d
		}
	}

	for _, d := range drafts {
		if len(d.undoRefs) == 0 {
			continue
		}

		before := ancestors(d, byName)

		for _, r := range d.undoRefs {
			if r != d.Name && !before[r] {
				c.violate(d.path+".undo_input", CodeUndoReference,
					fmt.Sprintf("step %s may not have run when %s is undone: add it to after", r, d.Name))
			}
		}
	}
}

func ancestors(d *draft, byName map[string]*draft) map[string]bool {
	out := map[string]bool{}

	stack := append([]string(nil), d.deps...)

	for len(stack) > 0 {
		name := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if out[name] || byName[name] == nil {
			continue
		}

		out[name] = true
		stack = append(stack, byName[name].deps...)
	}

	return out
}

// plan resolves the step's options: the binding's, else the activity's
// manifest defaults, else the platform's.
func (c *compiler) plan(d *draft, groups [][]string) StepPlan {
	p := StepPlan{
		Name: d.Name, Activity: d.Activity, Kind: d.act.GetKind(), Deps: slices.Clone(d.deps),
		Conditional: d.When != "", Undo: d.Undo,
		Retry: d.Retry, StartToClose: d.StartToClose, Heartbeat: d.Heartbeat,
	}
	p.Service, _ = SplitName(d.Activity)

	for i, g := range groups {
		if slices.Contains(g, d.Name) {
			p.Group = i
		}
	}

	if d.undo != nil {
		p.UndoService, _ = SplitName(d.Undo)
		p.UndoKind = d.undo.GetKind()
	}

	if p.StartToClose == 0 {
		p.StartToClose = durationOf(d.act.GetStartToClose())
	}

	if p.StartToClose == 0 {
		p.StartToClose = DefaultStartToClose
	}

	if p.Heartbeat == 0 {
		p.Heartbeat = durationOf(d.act.GetHeartbeat())
	}

	if p.Retry.Attempts == 0 {
		p.Retry.Attempts = int(d.act.GetRetry().GetAttempts())
	}

	if p.Retry.Attempts == 0 {
		p.Retry.Attempts = DefaultAttempts
	}

	if p.Retry.InitialInterval == 0 {
		p.Retry.InitialInterval = DefaultInitialInterval
	}

	if p.Retry.MaxInterval == 0 {
		p.Retry.MaxInterval = max(DefaultMaxInterval, p.Retry.InitialInterval)
	}

	if p.Retry.Backoff == 0 {
		p.Retry.Backoff = DefaultBackoff
	}

	return p
}

func schemaBytes(s *sp.Schema) []byte {
	if s == nil {
		return nil
	}

	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	if err != nil {
		return nil
	}

	return out
}

func schemaOf(b []byte) (*sp.Schema, error) {
	if len(b) == 0 {
		return nil, nil //nolint:nilnil // no schema
	}

	var s sp.Schema
	if err := proto.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("bindings: schema: %w", err)
	}

	return &s, nil
}

// errNoStep: a step name the program does not have.
var errNoStep = errors.New("bindings: no such step")
