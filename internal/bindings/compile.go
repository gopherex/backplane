package bindings

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
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

// Analysis is a definition as the compiler sees it: its violations, the
// order of its steps, the type of every value and what every expression
// reads. Parts that do not compile are left out.
type Analysis struct {
	Violations []Violation
	// Steps by name.
	Steps []StepAnalysis
	// Types by path.
	Types []ValueType
	// References by path, then position.
	References []Reference
}

// StepAnalysis is where a step stands in the order of execution.
type StepAnalysis struct {
	Name string
	// Level is 0 for a step that depends on nothing, else one above its
	// highest dependency; -1 in a cycle or with an invalid name.
	Level int
	// Data, After and When are the steps its input, `after` and `when`
	// make it depend on; Undo the steps its undo input reads.
	Data, After, When, Undo []string
}

// ValueType is the CEL type of a value node.
type ValueType struct {
	Path string
	Type string
}

// Reference is one read of a variable by the expression at Path.
type Reference struct {
	Path     string
	Variable string
	Fields   []string
	Expr     Range
}

// ValidateBinding checks b against the catalog: every violation, empty
// when valid. The error is a failure to validate, not a violation.
func ValidateBinding(b Binding, cat Catalog) ([]Violation, error) {
	_, a, err := compileBinding(b, cat)

	return a.Violations, err
}

// ValidateRule checks r against the catalog, as ValidateBinding.
func ValidateRule(r Rule, cat Catalog) ([]Violation, error) {
	_, a, err := compileRule(r, cat)

	return a.Violations, err
}

// AnalyzeBinding checks b against the catalog and describes it.
func AnalyzeBinding(b Binding, cat Catalog) (Analysis, error) {
	_, a, err := compileBinding(b, cat)

	return a, err
}

// AnalyzeRule checks r against the catalog and describes it.
func AnalyzeRule(r Rule, cat Catalog) (Analysis, error) {
	_, a, err := compileRule(r, cat)

	return a, err
}

// CompileBinding checks b against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileBinding(b Binding, cat Catalog) (*Program, error) {
	p, a, err := compileBinding(b, cat)
	if err != nil {
		return nil, err
	}

	if len(a.Violations) > 0 {
		return nil, &InvalidError{Violations: a.Violations}
	}

	return p, nil
}

// CompileRule checks r against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileRule(r Rule, cat Catalog) (*Program, error) {
	p, a, err := compileRule(r, cat)
	if err != nil {
		return nil, err
	}

	if len(a.Violations) > 0 {
		return nil, &InvalidError{Violations: a.Violations}
	}

	return p, nil
}

// Paths of the definition's own places.
const (
	pathHook   = "/hook"
	pathEvent  = "/event"
	pathWhen   = "/when"
	pathResult = "/result"
)

func compileBinding(b Binding, cat Catalog) (*Program, Analysis, error) {
	c := newCompiler(KindBinding, cat)

	var in, out *sp.Schema

	switch hook, ok := cat.Hook(b.Hook); {
	case !IsFullName(b.Hook):
		c.violate(pathHook, CodeInvalidName, fmt.Sprintf("%q is not a hook name <service>.<Hook>", b.Hook))
	case !ok:
		c.violate(pathHook, CodeUnknownHook, "no manifest declares hook "+b.Hook)
	default:
		in, out = hook.GetInput(), hook.GetOutput()
	}

	c.input = c.shapes.of(in, "bp.req")
	resultShape := c.shapes.of(out, "bp.result")

	return c.compile(source{kind: KindBinding, name: b.Hook, result: b.Result}, b.Steps, in, resultShape)
}

func compileRule(r Rule, cat Catalog) (*Program, Analysis, error) {
	c := newCompiler(KindRule, cat)

	var in *sp.Schema

	switch event, ok := cat.Event(r.Event); {
	case !IsFullName(r.Event):
		c.violate(pathEvent, CodeInvalidName, fmt.Sprintf("%q is not an event name <service>.<Event>", r.Event))
	case !ok:
		c.violate(pathEvent, CodeUnknownEvent, "no manifest declares event "+r.Event)
	default:
		in = event.GetSchema()
	}

	c.input = c.shapes.of(in, "bp.event")

	return c.compile(source{kind: KindRule, name: r.Event, when: r.When}, r.Steps, in, nil)
}

// source is what a definition runs on: a hook with its result, or an
// event with its filter.
type source struct {
	kind   Kind
	name   string
	when   string
	result Value
}

type compiler struct {
	kind     Kind
	cat      Catalog
	shapes   *shapes
	input    *shape
	env      *cel.Env
	analysis Analysis
}

func newCompiler(kind Kind, cat Catalog) *compiler {
	return &compiler{kind: kind, cat: cat, shapes: newShapes()}
}

func (c *compiler) violate(path, code, msg string) {
	c.violateAt(path, code, msg, Range{})
}

func (c *compiler) violateAt(path, code, msg string, at Range) {
	c.analysis.Violations = append(c.analysis.Violations, Violation{Path: path, Code: code, Message: msg, Expr: at})
}

// draft is a step while it compiles.
type draft struct {
	Step

	path     string
	ok       bool // name valid: a CEL variable
	act      *backplanev1.Activity
	undo     *backplanev1.Activity
	out      *sp.Schema
	outShape *shape
	data     []string
	after    []string
	when     []string
	undoRefs []string
	deps     []string
	level    int
}

func (c *compiler) compile(src source, steps []Step, in *sp.Schema, result *shape) (*Program, Analysis, error) {
	drafts := c.steps(steps)

	env, err := c.typedEnv(drafts)
	if err != nil {
		return nil, Analysis{}, err
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

	if src.kind == KindRule && src.when != "" {
		c.ruleWhen(src.when, names)
	}

	if src.kind == KindBinding {
		c.value(pathResult, src.result, result)
	}

	groups := c.order(drafts)
	c.undoRefs(drafts)
	c.describe(drafts)

	if len(c.analysis.Violations) > 0 {
		return nil, c.analysis, nil
	}

	s := spec{Kind: src.kind, Source: src.name, When: src.when, Result: src.result.JSON(), Input: schemaBytes(in)}

	for _, d := range drafts {
		s.Steps = append(s.Steps, specStep{
			StepPlan: c.plan(d, groups), When: d.When, Input: d.Input.JSON(), UndoInput: d.UndoInput.JSON(),
			Output: schemaBytes(d.out),
		})
	}

	p, err := newProgram(s)
	if err != nil {
		return nil, Analysis{}, err
	}

	return p, c.analysis, nil
}

// ruleWhen compiles a rule's filter: a bool over event and meta only.
func (c *compiler) ruleWhen(src string, names map[string]bool) {
	t, rs, ok := c.expr(pathWhen, src)
	if ok {
		c.boolean(pathWhen, t)
	}

	for _, r := range rs {
		if r.variable == VarSteps || names[r.variable] {
			c.violateAt(pathWhen, CodeUnknownStep, "a rule's when sees only event and meta, not steps", r.whole)

			return
		}
	}
}

// steps checks what does not need CEL: names, activities, options.
func (c *compiler) steps(steps []Step) []*draft {
	sorted := sortSteps(slices.Clone(steps))
	out := make([]*draft, 0, len(sorted))

	for i := range sorted {
		s := &sorted[i]
		d := &draft{Step: *s, path: Pointer("steps", s.Name), level: -1}
		out = append(out, d)

		switch {
		case !IsIdent(s.Name):
			c.violate(d.path, CodeInvalidName, fmt.Sprintf("%q is not an identifier [A-Za-z_][A-Za-z0-9_]*", s.Name))
		case reserved[s.Name]:
			c.violate(d.path, CodeReservedName, fmt.Sprintf("%q is reserved", s.Name))
		default:
			d.ok = true
		}

		d.act = c.activity(d.path+"/activity", s.Activity)

		if d.act != nil {
			d.out = d.act.GetOutput()
		}

		if s.Undo != "" {
			d.undo = c.activity(d.path+"/undo", s.Undo)
		} else if !s.UndoInput.IsZero() {
			c.violate(d.path+"/undoInput", CodeInvalidOption, "undo input without an undo activity")
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
		c.violate(d.path+"/retry/attempts", CodeInvalidOption, "attempts must not be negative")
	}

	if r.InitialInterval < 0 {
		c.violate(d.path+"/retry/initialInterval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval < 0 {
		c.violate(d.path+"/retry/maxInterval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval > 0 && r.InitialInterval > r.MaxInterval {
		c.violate(d.path+"/retry/maxInterval", CodeInvalidOption, "must not be below the initial interval")
	}

	if r.Backoff != 0 && r.Backoff < 1 {
		c.violate(d.path+"/retry/backoff", CodeInvalidOption, "must be at least 1")
	}

	if d.StartToClose < 0 {
		c.violate(d.path+"/startToClose", CodeInvalidOption, "must not be negative")
	}

	if d.Heartbeat < 0 {
		c.violate(d.path+"/heartbeat", CodeInvalidOption, "must not be negative")
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
	if d.When != "" {
		t, rs, ok := c.expr(d.path+"/when", d.When)
		if ok {
			c.boolean(d.path+"/when", t)
		}

		d.when = c.stepReads(d, rs, names)
	}

	var in *shape
	if d.act != nil {
		in = c.shapes.of(d.act.GetInput(), "bp.in."+d.Name)
	}

	d.data = c.stepReads(d, c.value(d.path+"/input", d.Input, in), names)

	if d.Undo != "" && !d.UndoInput.IsZero() {
		var undoIn *shape
		if d.undo != nil {
			undoIn = c.shapes.of(d.undo.GetInput(), "bp.undo."+d.Name)
		}

		d.undoRefs = stepNames(c.value(d.path+"/undoInput", d.UndoInput, undoIn), names)
	}

	for i, a := range d.After {
		path := d.path + "/after/" + strconv.Itoa(i)

		switch {
		case a == d.Name:
			c.violate(path, CodeCycle, fmt.Sprintf("step %s runs after itself", a))
		case !names[a]:
			c.violate(path, CodeUnknownStep, "no step "+a)
		default:
			d.after = appendNew(d.after, a)
		}
	}

	for _, list := range [][]string{d.after, d.data, d.when} {
		for _, dep := range list {
			d.deps = appendNew(d.deps, dep)
		}
	}
}

// stepReads are the steps among reads, a read of the step's own output
// reported.
func (c *compiler) stepReads(d *draft, rs []read, names map[string]bool) []string {
	var out []string

	for _, r := range rs {
		name, ok := stepOf(r, names)
		if !ok {
			continue
		}

		if name == d.Name {
			c.violateAt(r.path, CodeCycle, fmt.Sprintf("step %s reads its own output", name), r.whole)

			continue
		}

		out = appendNew(out, name)
	}

	return out
}

// stepOf is the step a read is of: its variable, or the field selected on
// `steps`.
func stepOf(r read, names map[string]bool) (string, bool) {
	name := r.variable
	if name == VarSteps {
		if len(r.fields) == 0 {
			return "", false
		}

		name = r.fields[0]
	}

	return name, names[name]
}

// stepNames are the steps among reads.
func stepNames(rs []read, names map[string]bool) []string {
	var out []string

	for _, r := range rs {
		if name, ok := stepOf(r, names); ok {
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

// value compiles a Value against the shape it must have (nil: unknown, no
// check) and returns what its expressions read. Unset is {} of want.
func (c *compiler) value(path string, v Value, want *shape) []read {
	switch v.Kind() {
	case ValueUnset:
		c.required(path, want, nil)

		return nil
	case ValueExpr:
		t, reads, ok := c.expr(path, v.Expr())
		if ok && want != nil {
			if msg := c.assignable(want, t); msg != "" {
				c.violate(path, CodeTypeMismatch, msg)
			}
		}

		return reads
	case ValueObject:
		return c.object(path, v, want)
	case ValueList:
		return c.list(path, v, want)
	default:
		c.literal(path, v.Literal(), want)

		return nil
	}
}

func (c *compiler) object(path string, v Value, want *shape) []read {
	c.typed(path, "object")

	fields := v.Fields()
	reads := make([]read, 0, len(fields))
	set := map[string]bool{}

	for _, f := range fields {
		set[f.Name] = true
	}

	for _, f := range fields {
		fpath := path + "/" + escapePointer(f.Name)

		var fieldWant *shape

		switch {
		case want == nil:
		case want.kind == kindObject:
			if fieldWant = want.fields[f.Name]; fieldWant == nil {
				c.violate(fpath, CodeUnknownField, "the schema declares no field "+f.Name)
			}
		case want.kind == kindMap:
			fieldWant = want.elem
		default:
		}

		reads = append(reads, c.value(fpath, f.Value, fieldWant)...)
	}

	if want != nil && want.kind != kindObject && want.kind != kindMap && want.kind != kindDyn {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got an object", want))
	}

	c.required(path, want, set)

	return reads
}

// required reports the required fields of an object shape not in set.
func (c *compiler) required(path string, want *shape, set map[string]bool) {
	if want == nil || want.kind != kindObject {
		return
	}

	for _, r := range want.required {
		if !set[r] {
			c.violate(path, CodeMissingField, fmt.Sprintf("required field %s is not set", r))
		}
	}
}

func (c *compiler) list(path string, v Value, want *shape) []read {
	c.typed(path, "list")

	var elem *shape

	switch {
	case want == nil, want.kind == kindDyn:
	case want.kind == kindList:
		elem = want.elem
	default:
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got a list", want))
	}

	items := v.Items()
	reads := make([]read, 0, len(items))

	for i, item := range items {
		reads = append(reads, c.value(path+"/"+strconv.Itoa(i), item, elem)...)
	}

	return reads
}

// Literal kinds as the analysis names them.
const (
	typeInt    = "int"
	typeDouble = "double"
	typeBool   = "bool"
	typeNull   = "null"
)

// literal checks a number, bool or null against want. A string is an
// expression, never a literal: a literal string is written as CEL
// ("'text'").
func (c *compiler) literal(path string, v any, want *shape) {
	got := literalType(v)
	c.typed(path, got)

	if got == typeNull || want == nil {
		return
	}

	if want.kind == kindString {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected string, got %s (a string is an expression: "+
			"write a literal as '%v' in quotes)", got, v))

		return
	}

	if !literalFits(want.kind, got, v) {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got %s", want, got))
	}
}

func literalType(v any) string {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return typeInt
		}

		return typeDouble
	case bool:
		return typeBool
	default:
		return typeNull
	}
}

func literalFits(want kind, got string, v any) bool {
	switch want {
	case kindDyn:
		return true
	case kindDouble:
		return got != typeBool
	case kindInt:
		return got == typeInt
	case kindUint:
		n, isNum := v.(float64)

		return got == typeInt && isNum && n >= 0
	case kindBool:
		return got == typeBool
	default:
		return false
	}
}

func (c *compiler) typed(path, t string) {
	c.analysis.Types = append(c.analysis.Types, ValueType{Path: path, Type: t})
}

// expr compiles src in the typed environment: its output type and what it
// reads (also when it does not type-check); a failure is a violation.
func (c *compiler) expr(path, src string) (*types.Type, []read, bool) {
	if strings.TrimSpace(src) == "" {
		c.violate(path, CodeCEL, "empty expression")

		return nil, nil, false
	}

	parsed, iss := c.env.Parse(src)
	if iss.Err() != nil {
		for _, e := range iss.Errors() {
			c.violateAt(path, CodeCEL, e.Message, issueRange(src, nil, e))
		}

		return nil, nil, false
	}

	native := parsed.NativeRep()
	reads := readsOf(src, native)

	for i := range reads {
		reads[i].path = path
	}

	for _, r := range reads {
		c.analysis.References = append(c.analysis.References, Reference{
			Path: path, Variable: r.variable, Fields: slices.Clone(r.fields), Expr: r.whole,
		})
	}

	checked, iss := c.env.Check(parsed)
	if iss.Err() != nil {
		for _, e := range iss.Errors() {
			c.violateAt(path, CodeCEL, e.Message, issueRange(src, native, e))
		}

		return nil, reads, false
	}

	if est, err := c.env.EstimateCost(checked, sizeEstimator{}); err == nil && est.Max > costLimit {
		c.violate(path, CodeCost, fmt.Sprintf("may cost up to %s evaluation units, over the limit of %d "+
			"(assuming collections of up to %d items)", costText(est.Max), costLimit, maxCollection))
	}

	c.typed(path, typeName(checked.OutputType()))

	return checked.OutputType(), reads, true
}

func costText(n uint64) string {
	if n == math.MaxUint64 {
		return "unbounded"
	}

	return strconv.FormatUint(n, 10)
}

// typeName is a CEL type as the editor shows it: objects of the schemas
// are "object".
func typeName(t *types.Type) string {
	switch t.Kind() {
	case types.StructKind:
		return "object"
	case types.ListKind:
		return "list(" + typeName(t.Parameters()[0]) + ")"
	case types.MapKind:
		return "map(" + typeName(t.Parameters()[0]) + ", " + typeName(t.Parameters()[1]) + ")"
	case types.NullTypeKind:
		return typeNull
	case types.OpaqueKind:
		if t.TypeName() == "optional_type" && len(t.Parameters()) == 1 {
			return "optional(" + typeName(t.Parameters()[0]) + ")"
		}
	default:
	}

	return t.String()
}

func (c *compiler) boolean(path string, t *types.Type) {
	switch t.Kind() {
	case types.BoolKind, types.DynKind, types.AnyKind, types.TypeParamKind:
	default:
		c.violate(path, CodeTypeMismatch, "expected bool, got "+typeName(t))
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

	mismatch := fmt.Sprintf("expected %s, got %s", want, typeName(t))

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
		return c.objectType(want, t, mismatch)
	}

	return mismatch
}

// objectType checks a value against an object shape field by field when
// both are known objects.
func (c *compiler) objectType(want *shape, t *types.Type, mismatch string) string {
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

// order sets the steps' levels from their dependencies (Kahn's algorithm,
// by name within a level) and reports a cycle; the groups are the steps
// by level.
func (c *compiler) order(drafts []*draft) [][]string {
	pending := 0

	for _, d := range drafts {
		if d.ok {
			pending++
		}
	}

	level := map[string]int{}

	var groups [][]string

	for len(level) < pending {
		var ready []*draft

		for _, d := range drafts {
			if _, done := level[d.Name]; done || !d.ok {
				continue
			}

			if allIn(d.deps, level) {
				ready = append(ready, d)
			}
		}

		if len(ready) == 0 {
			c.cycle(drafts, level)

			return groups
		}

		var names []string

		for _, d := range ready {
			level[d.Name] = len(groups)
			d.level = len(groups)
			names = append(names, d.Name)
		}

		groups = append(groups, names)
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

	var start *draft

	for _, d := range drafts {
		if _, done := level[d.Name]; !done && d.ok {
			byName[d.Name] = d
			if start == nil {
				start = d
			}
		}
	}

	var path []string

	seen := map[string]int{}

	// Every step left has a dependency left: walking them must return.
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
				c.violate(d.path+"/undoInput", CodeUndoReference,
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

// describe fills the analysis' steps and orders its lists.
func (c *compiler) describe(drafts []*draft) {
	for _, d := range drafts {
		c.analysis.Steps = append(c.analysis.Steps, StepAnalysis{
			Name: d.Name, Level: d.level, Data: d.data, After: d.after, When: d.when, Undo: d.undoRefs,
		})
	}

	slices.SortStableFunc(c.analysis.Types, func(a, b ValueType) int { return strings.Compare(a.Path, b.Path) })
	slices.SortStableFunc(c.analysis.References, func(a, b Reference) int {
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}

		return a.Expr.Start - b.Expr.Start
	})
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
