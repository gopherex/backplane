package bindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/cel-go/cel"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// Kind is what a program implements.
type Kind int

// Kinds of programs.
const (
	// KindBinding implements a hook: input `req`, a result.
	KindBinding Kind = iota + 1
	// KindRule reacts to an event: input `event` and `meta`, a filter, no
	// result.
	KindRule
)

// StepPlan is a step as the executor runs it: the activity, where it
// runs, what it waits for and its resolved options.
type StepPlan struct {
	Name     string `json:"name"`
	Activity string `json:"activity"`
	// Service owns the activity: its task queue (§17).
	Service string `json:"service"`
	// Kind: a Temporal activity, or a workflow run as a child workflow.
	Kind backplanev1.ActivityKind `json:"kind"`
	// Deps are the steps this one waits for, in declaration order: its
	// `after` and every step its input and when read.
	Deps []string `json:"deps,omitempty"`
	// Group is the step's level in Program.Groups: every dependency is in
	// a lower group.
	Group int `json:"group"`
	// Conditional: the step has a when (Program.When decides).
	Conditional bool `json:"conditional,omitempty"`
	// Undo is the compensating activity, empty for none; UndoService and
	// UndoKind as Service and Kind.
	Undo        string                   `json:"undo,omitempty"`
	UndoService string                   `json:"undo_service,omitempty"`
	UndoKind    backplanev1.ActivityKind `json:"undo_kind,omitempty"`
	// Retry is resolved: every field set.
	Retry        Retry         `json:"retry"`
	StartToClose time.Duration `json:"start_to_close"`
	// Heartbeat 0: none.
	Heartbeat time.Duration `json:"heartbeat,omitempty"`
}

// spec is a Program's serialized form: everything evaluation needs,
// nothing it has to look up.
type spec struct {
	Kind   Kind       `json:"kind"`
	Source string     `json:"source"`
	When   string     `json:"when,omitempty"`
	Steps  []specStep `json:"steps"`
	Result Value      `json:"result"`
	// Input is the schema of `req` or `event` (protobuf binary).
	Input []byte `json:"input,omitempty"`
}

type specStep struct {
	StepPlan

	When      string `json:"when_expr,omitempty"`
	Input     Value  `json:"input"`
	UndoInput Value  `json:"undo_input"`
	// Output is the schema of the activity's output (protobuf binary).
	Output []byte `json:"output,omitempty"`
}

// Program is a compiled binding or rule: the steps ordered into groups,
// options resolved, expressions compiled. It is built whole — by Compile
// or by json.Unmarshal of a marshaled Program — and not modified after:
// share it freely.
//
// Evaluation is pure and deterministic: expressions read only the Scope,
// nothing reads a clock or randomness, maps are iterated in key order and
// results are JSON with sorted keys, so the same Program and Scope give
// the same bytes on every run — it is safe inside Temporal workflow code.
type Program struct {
	spec   spec
	env    *cel.Env
	input  *shape
	groups [][]string
	index  map[string]int
	steps  []runStep
	when   cel.Program
	result valueProgram
}

type runStep struct {
	when      cel.Program
	input     valueProgram
	undoInput valueProgram
	output    *shape
}

// valueProgram is a compiled Value: fields, or one program; neither is
// the default of its place.
type valueProgram struct {
	names  []string
	fields []cel.Program
	whole  cel.Program
}

func (v valueProgram) zero() bool { return v.whole == nil && v.fields == nil }

// newProgram builds the evaluation side of a spec.
func newProgram(s spec) (*Program, error) {
	p := &Program{spec: s, index: map[string]int{}}

	vars := []string{VarSteps}
	if s.Kind == KindRule {
		vars = append(vars, VarEvent, VarMeta)
	} else {
		vars = append(vars, VarReq)
	}

	for i := range s.Steps {
		st := &s.Steps[i]
		p.index[st.Name] = i
		vars = append(vars, st.Name)

		for len(p.groups) <= st.Group {
			p.groups = append(p.groups, nil)
		}

		p.groups[st.Group] = append(p.groups[st.Group], st.Name)
	}

	env, err := runtimeEnv(vars)
	if err != nil {
		return nil, err
	}

	p.env = env
	b := newShapes()

	in, err := schemaOf(s.Input)
	if err != nil {
		return nil, err
	}

	p.input = b.of(in, "bp.input")

	if s.When != "" {
		if p.when, err = program(env, s.When); err != nil {
			return nil, fmt.Errorf("bindings: when: %w", err)
		}
	}

	if p.result, err = p.value(s.Result); err != nil {
		return nil, fmt.Errorf("bindings: result: %w", err)
	}

	for i := range s.Steps {
		run, err := p.step(b, &s.Steps[i])
		if err != nil {
			return nil, fmt.Errorf("bindings: step %s: %w", s.Steps[i].Name, err)
		}

		p.steps = append(p.steps, run)
	}

	return p, nil
}

func (p *Program) step(b *shapes, st *specStep) (runStep, error) {
	var (
		run runStep
		err error
	)

	if st.When != "" {
		if run.when, err = program(p.env, st.When); err != nil {
			return run, fmt.Errorf("when: %w", err)
		}
	}

	if run.input, err = p.value(st.Input); err != nil {
		return run, fmt.Errorf("input: %w", err)
	}

	if run.undoInput, err = p.value(st.UndoInput); err != nil {
		return run, fmt.Errorf("undo input: %w", err)
	}

	out, err := schemaOf(st.Output)
	if err != nil {
		return run, err
	}

	run.output = b.of(out, "bp.step."+st.Name)

	return run, nil
}

func (p *Program) value(v Value) (valueProgram, error) {
	if v.Expr != "" {
		prg, err := program(p.env, v.Expr)

		return valueProgram{whole: prg}, err
	}

	var out valueProgram

	for _, f := range v.Fields {
		prg, err := program(p.env, f.Expr)
		if err != nil {
			return out, fmt.Errorf("%s: %w", f.Name, err)
		}

		out.names = append(out.names, f.Name)
		out.fields = append(out.fields, prg)
	}

	return out, nil
}

// MarshalJSON is the program's serialized form: a workflow takes it as
// input and evaluates exactly what was compiled.
func (p *Program) MarshalJSON() ([]byte, error) {
	// Value, Field and Retry go by their Go field names: tagging them would
	// change the input of the workflows in flight.
	out, err := json.Marshal(p.spec) //nolint:musttag // see above
	if err != nil {
		return nil, fmt.Errorf("bindings: program: %w", err)
	}

	return out, nil
}

// UnmarshalJSON rebuilds a marshaled program. Only for a new zero Program
// (json.Unmarshal into one): a Program is not modified once built.
func (p *Program) UnmarshalJSON(data []byte) error {
	var s spec
	if err := json.Unmarshal(data, &s); err != nil { //nolint:musttag // untagged types go by Go field names
		return fmt.Errorf("bindings: program: %w", err)
	}

	built, err := newProgram(s)
	if err != nil {
		return err
	}

	*p = *built

	return nil
}

// Kind is what the program implements.
func (p *Program) Kind() Kind { return p.spec.Kind }

// Source is the hook or event name.
func (p *Program) Source() string { return p.spec.Source }

// Steps are the steps in declaration order.
func (p *Program) Steps() []StepPlan {
	out := make([]StepPlan, 0, len(p.spec.Steps))

	for i := range p.spec.Steps {
		plan := p.spec.Steps[i].StepPlan
		plan.Deps = append([]string(nil), plan.Deps...)
		out = append(out, plan)
	}

	return out
}

// Step is one step by name.
func (p *Program) Step(name string) (StepPlan, bool) {
	i, ok := p.index[name]
	if !ok {
		return StepPlan{}, false
	}

	plan := p.spec.Steps[i].StepPlan
	plan.Deps = append([]string(nil), plan.Deps...)

	return plan, true
}

// Groups are the steps by level: a group's steps depend only on steps of
// lower groups and may run in parallel; within a group in declaration
// order. Running group after group is a valid order; running each step
// once its Deps are done is a finer one.
func (p *Program) Groups() [][]string {
	out := make([][]string, 0, len(p.groups))
	for _, g := range p.groups {
		out = append(out, append([]string(nil), g...))
	}

	return out
}

// Meta are the CloudEvents attributes of the event a rule runs on, the CEL
// variable `meta` (time as RFC 3339).
type Meta struct {
	ID      string
	Source  string
	Subject string
	Type    string
	Time    time.Time
}

// Scope is what expressions see at one point of a run: the input, the
// outputs of the steps done so far and which steps were skipped. It is a
// value: Bind and Skip return a new Scope and leave the old one as it
// was.
type Scope struct {
	vars map[string]any
}

// Vars is a copy of the scope's variables (values as CEL sees them).
func (s Scope) Vars() map[string]any { return maps.Clone(s.vars) }

// Start is the scope of a binding's run on the hook's input (JSON).
func (p *Program) Start(input []byte) (Scope, error) {
	if p.spec.Kind != KindBinding {
		return Scope{}, errors.New("bindings: Start of a rule program: use StartEvent") //nolint:err113 // misuse
	}

	req, err := decodeJSON(input)
	if err != nil {
		return Scope{}, &EvalError{Place: "input", Err: err}
	}

	return Scope{vars: p.initial(VarReq, convert(p.input, req))}, nil
}

// StartEvent is the scope of a rule's run on an event: its payload (JSON)
// and attributes.
func (p *Program) StartEvent(payload []byte, meta Meta) (Scope, error) {
	if p.spec.Kind != KindRule {
		return Scope{}, errors.New("bindings: StartEvent of a binding program: use Start") //nolint:err113 // misuse
	}

	ev, err := decodeJSON(payload)
	if err != nil {
		return Scope{}, &EvalError{Place: "event", Err: err}
	}

	vars := p.initial(VarEvent, convert(p.input, ev))

	var eventTime string
	if !meta.Time.IsZero() {
		eventTime = meta.Time.UTC().Format(time.RFC3339Nano)
	}

	vars[VarMeta] = map[string]any{
		"id": meta.ID, "source": meta.Source, "subject": meta.Subject, "type": meta.Type, "time": eventTime,
	}

	return Scope{vars: vars}, nil
}

func (p *Program) initial(name string, value any) map[string]any {
	steps := make(map[string]any, len(p.spec.Steps))
	vars := map[string]any{name: value, VarSteps: steps}

	for i := range p.spec.Steps {
		step := p.spec.Steps[i].Name
		steps[step] = map[string]any{"skipped": false}
		vars[step] = map[string]any{}
	}

	return vars
}

// Bind is s with the output (JSON) of a step that ran.
func (p *Program) Bind(s Scope, step string, output []byte) (Scope, error) {
	i, ok := p.index[step]
	if !ok {
		return Scope{}, fmt.Errorf("%w: %s", errNoStep, step)
	}

	v, err := decodeJSON(output)
	if err != nil {
		return Scope{}, &EvalError{Step: step, Place: "output", Err: err}
	}

	return s.with(step, convert(p.steps[i].output, v), false), nil
}

// Skip is s with a step skipped by its when: its variable is {} and
// steps.<name>.skipped is true. Steps depending on it still run.
func (p *Program) Skip(s Scope, step string) (Scope, error) {
	if _, ok := p.index[step]; !ok {
		return Scope{}, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return s.with(step, map[string]any{}, true), nil
}

func (s Scope) with(step string, value any, skipped bool) Scope {
	vars := maps.Clone(s.vars)
	vars[step] = value

	steps, _ := vars[VarSteps].(map[string]any)
	steps = maps.Clone(steps)
	steps[step] = map[string]any{"skipped": skipped}
	vars[VarSteps] = steps

	return Scope{vars: vars}
}

// Match is a rule's when on the scope of StartEvent: false — ack the event
// without a run. A binding, or a rule without when, matches.
func (p *Program) Match(s Scope) (bool, error) {
	if p.when == nil {
		return true, nil
	}

	return evalBool(p.when, s.vars, "", "when")
}

// When is the step's when on s; a step without one runs.
func (p *Program) When(step string, s Scope) (bool, error) {
	i, ok := p.index[step]
	if !ok {
		return false, fmt.Errorf("%w: %s", errNoStep, step)
	}

	if p.steps[i].when == nil {
		return true, nil
	}

	return evalBool(p.steps[i].when, s.vars, step, "when")
}

// Input is the step's activity input (JSON) on s.
func (p *Program) Input(step string, s Scope) ([]byte, error) {
	i, ok := p.index[step]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return evalValue(p.steps[i].input, s.vars, step, "input", map[string]any{})
}

// UndoInput is the input (JSON) of the step's undo activity on s: its
// undo input, else the step's own output.
func (p *Program) UndoInput(step string, s Scope) ([]byte, error) {
	i, ok := p.index[step]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return evalValue(p.steps[i].undoInput, s.vars, step, "undo input", s.vars[step])
}

// Result is a binding's output (JSON) on s: the hook's answer.
func (p *Program) Result(s Scope) ([]byte, error) {
	return evalValue(p.result, s.vars, "", "result", map[string]any{})
}

// Eval evaluates src in the program's environment over vars (a Scope's
// Vars, possibly changed): the console's playground, tests. src is
// compiled on every call.
func (p *Program) Eval(src string, vars map[string]any) (any, error) {
	prg, err := program(p.env, src)
	if err != nil {
		return nil, err
	}

	return eval(prg, vars)
}

// EvalError is an expression that failed on the values of a run:
// "transform failed at step render (input.to): no such key: x".
type EvalError struct {
	// Step is empty for the result and a rule's when.
	Step string
	// Place: when, input, input.<field>, undo input, output, result, event.
	Place string
	Err   error
}

func (e *EvalError) Error() string {
	if e.Step == "" {
		return fmt.Sprintf("transform failed at %s: %v", e.Place, e.Err)
	}

	return fmt.Sprintf("transform failed at step %s (%s): %v", e.Step, e.Place, e.Err)
}

func (e *EvalError) Unwrap() error { return e.Err }

func evalBool(prg cel.Program, vars map[string]any, step, place string) (bool, error) {
	out, err := eval(prg, vars)
	if err != nil {
		return false, &EvalError{Step: step, Place: place, Err: err}
	}

	b, ok := out.(bool)
	if !ok {
		return false, &EvalError{Step: step, Place: place, Err: fmt.Errorf("%w: got %T", errNotBool, out)}
	}

	return b, nil
}

var errNotBool = errors.New("not a bool")

func evalValue(v valueProgram, vars map[string]any, step, place string, zero any) ([]byte, error) {
	var out any

	switch {
	case v.zero():
		out = zero
	case v.whole != nil:
		value, err := eval(v.whole, vars)
		if err != nil {
			return nil, &EvalError{Step: step, Place: place, Err: err}
		}

		out = value
	default:
		obj := make(map[string]any, len(v.fields))

		for i, prg := range v.fields {
			value, err := eval(prg, vars)
			if err != nil {
				return nil, &EvalError{Step: step, Place: place + "." + v.names[i], Err: err}
			}

			obj[v.names[i]] = value
		}

		out = obj
	}

	data, err := marshal(out)
	if err != nil {
		return nil, &EvalError{Step: step, Place: place, Err: err}
	}

	return data, nil
}

// marshal is deterministic JSON: sorted keys, no HTML escaping.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
