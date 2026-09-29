package bindings

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/google/cel-go/parser"
)

// Limits of an expression.
const (
	// maxExprBytes bounds the source of one expression.
	maxExprBytes = 16 << 10
	// costLimit bounds one evaluation (CEL's cost units): a runaway
	// comprehension fails instead of stalling a workflow task.
	costLimit = 1_000_000
)

// Arities of the comprehension macros.
const (
	// iterArgs: the iteration variable and the body.
	iterArgs = 2
	// transformArgs: the iteration variable, the filter and the transform.
	transformArgs = 3
)

// orderedFn wraps the range of every comprehension macro: a map is
// iterated by its keys in order, so all/exists/map/filter over a map give
// the same result on every run (Go's map order is random).
const orderedFn = "@ordered"

// library is the language of binding expressions: standard CEL with the
// strings, encoders, math and lists extensions and optional values;
// numbers compare across int, uint and double. Nothing in it reads the
// clock, randomness or the outside world.
func library() []cel.EnvOption {
	iterate := func(expand parser.MacroExpander) parser.MacroExpander {
		return func(eh parser.ExprHelper, target ast.Expr, args []ast.Expr) (ast.Expr, *common.Error) {
			return expand(eh, eh.NewCall(orderedFn, target), args)
		}
	}

	k, v := cel.TypeParamType("K"), cel.TypeParamType("V")

	return []cel.EnvOption{
		cel.ClearMacros(),
		cel.Macros(
			cel.HasMacro,
			parser.NewReceiverMacro("all", iterArgs, iterate(parser.MakeAll)),
			parser.NewReceiverMacro("exists", iterArgs, iterate(parser.MakeExists)),
			parser.NewReceiverMacro("exists_one", iterArgs, iterate(parser.MakeExistsOne)),
			parser.NewReceiverMacro("map", iterArgs, iterate(parser.MakeMap)),
			parser.NewReceiverMacro("map", transformArgs, iterate(parser.MakeMap)),
			parser.NewReceiverMacro("filter", iterArgs, iterate(parser.MakeFilter)),
		),
		ext.Strings(), ext.Encoders(), ext.Math(), ext.Lists(),
		cel.OptionalTypes(),
		cel.CrossTypeNumericComparisons(true),
		cel.ParserExpressionSizeLimit(maxExprBytes),
		cel.Function(orderedFn,
			cel.Overload("bp_ordered_list", []*cel.Type{cel.ListType(v)}, cel.ListType(v)),
			cel.Overload("bp_ordered_map", []*cel.Type{cel.MapType(k, v)}, cel.ListType(k)),
			cel.SingletonUnaryBinding(ordered)),
	}
}

// ordered is a list as is and a map as its keys in order.
func ordered(v ref.Val) ref.Val {
	switch x := v.(type) {
	case traits.Lister:
		return v
	case traits.Mapper:
		var keys []ref.Val

		for it := x.Iterator(); it.HasNext() == types.True; {
			keys = append(keys, it.Next())
		}

		slices.SortFunc(keys, compareKeys)

		return types.NewRefValList(types.DefaultTypeAdapter, keys)
	default:
		return types.NewErr("cannot iterate over %s", v.Type().TypeName())
	}
}

func compareKeys(a, b ref.Val) int {
	ta, tb := a.Type().TypeName(), b.Type().TypeName()
	if ta != tb {
		return cmp.Compare(ta, tb)
	}

	if c, ok := a.(traits.Comparer); ok {
		if n, isInt := c.Compare(b).(types.Int); isInt {
			return int(n)
		}
	}

	return 0
}

// baseEnv is the library without variables: every evaluation environment
// extends it.
//
//nolint:gochecknoglobals // one immutable environment per process
var baseEnv = sync.OnceValues(func() (*cel.Env, error) {
	env, err := cel.NewEnv(library()...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
})

// runtimeEnv declares every variable dyn: evaluation checks nothing
// statically — the save did — and dispatches on the values.
func runtimeEnv(vars []string) (*cel.Env, error) {
	base, err := baseEnv()
	if err != nil {
		return nil, err
	}

	opts := make([]cel.EnvOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}

	env, err := base.Extend(opts...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
}

// typedEnv declares the variables with the types of their shapes and
// knows the object types among them.
func typedEnv(vars map[string]*types.Type, objects map[string]*shape) (*cel.Env, error) {
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("bindings: cel types: %w", err)
	}

	opts := []cel.EnvOption{cel.CustomTypeProvider(&provider{Registry: reg, objects: objects})}
	opts = append(opts, library()...)

	for _, name := range sortedKeys(vars) {
		opts = append(opts, cel.Variable(name, vars[name]))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
}

// program compiles src into an evaluable program of env.
func program(env *cel.Env, src string) (cel.Program, error) {
	checked, iss := env.Compile(src)
	if iss.Err() != nil {
		return nil, fmt.Errorf("%w", iss.Err())
	}

	prg, err := env.Program(checked, cel.CostLimit(costLimit))
	if err != nil {
		return nil, fmt.Errorf("cel: %w", err)
	}

	return prg, nil
}

// eval runs prg over vars into a JSON-able value.
func eval(prg cel.Program, vars map[string]any) (any, error) {
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return native(out)
}

// errNotJSON: a value CEL produced has no JSON form.
var errNotJSON = errors.New("value has no JSON form")

// native is a CEL value as JSON would carry it: objects map[string]any
// (keys of other types in their text form), lists []any, int64, uint64,
// float64, string, bool, nil, bytes []byte (base64 in JSON), timestamps
// RFC 3339 strings, durations protojson strings ("1.5s"), an optional
// its value or null.
//
//nolint:cyclop // one case per CEL type
func native(v ref.Val) (any, error) {
	switch x := v.(type) {
	case types.Null:
		return nil, nil //nolint:nilnil // nil is JSON null, a valid value
	case types.Bool:
		return bool(x), nil
	case types.Int:
		return int64(x), nil
	case types.Uint:
		return uint64(x), nil
	case types.Double:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: %v", errNotJSON, f)
		}

		return f, nil
	case types.String:
		return string(x), nil
	case types.Bytes:
		return []byte(x), nil
	case types.Timestamp:
		return x.UTC().Format(time.RFC3339Nano), nil
	case types.Duration:
		return strconv.FormatFloat(x.Seconds(), 'f', -1, 64) + "s", nil
	case *types.Optional:
		if !x.HasValue() {
			return nil, nil //nolint:nilnil // an empty optional is JSON null
		}

		return native(x.GetValue())
	case *types.Err:
		return nil, x
	case traits.Lister:
		return nativeList(x)
	case traits.Mapper:
		return nativeMap(x)
	default:
		if v.Type() == types.NullType {
			return nil, nil //nolint:nilnil // nil is JSON null, a valid value
		}

		out, err := v.ConvertToNative(reflect.TypeFor[any]())
		if err != nil {
			return nil, fmt.Errorf("%w: %s", errNotJSON, v.Type())
		}

		return out, nil
	}
}

func nativeList(l traits.Lister) ([]any, error) {
	n, _ := l.Size().(types.Int)
	out := make([]any, 0, int(n))

	for i := range int64(n) {
		item, err := native(l.Get(types.Int(i)))
		if err != nil {
			return nil, err
		}

		out = append(out, item)
	}

	return out, nil
}

func nativeMap(m traits.Mapper) (map[string]any, error) {
	out := map[string]any{}

	for it := m.Iterator(); it.HasNext() == types.True; {
		k := it.Next()

		key, err := native(k)
		if err != nil {
			return nil, err
		}

		value, err := native(m.Get(k))
		if err != nil {
			return nil, err
		}

		out[fmt.Sprint(key)] = value
	}

	return out, nil
}

// refs are the names an expression reads: identifiers not bound by a
// comprehension, and "steps.<name>" for a select on `steps`.
func refs(e ast.Expr) []string {
	var out []string

	walk(e, map[string]int{}, func(name string) {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	})

	return out
}

func walk(e ast.Expr, bound map[string]int, visit func(string)) {
	switch e.Kind() {
	case ast.IdentKind:
		if bound[e.AsIdent()] == 0 {
			visit(e.AsIdent())
		}
	case ast.SelectKind:
		walkSelect(e.AsSelect(), bound, visit)
	case ast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			walk(call.Target(), bound, visit)
		}

		walkAll(call.Args(), bound, visit)
	case ast.ListKind:
		walkAll(e.AsList().Elements(), bound, visit)
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			walk(entry.AsMapEntry().Key(), bound, visit)
			walk(entry.AsMapEntry().Value(), bound, visit)
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			walk(f.AsStructField().Value(), bound, visit)
		}
	case ast.ComprehensionKind:
		walkComprehension(e.AsComprehension(), bound, visit)
	default:
	}
}

func walkAll(exprs []ast.Expr, bound map[string]int, visit func(string)) {
	for _, e := range exprs {
		walk(e, bound, visit)
	}
}

// walkSelect reports a select on an unbound `steps` as "steps.<name>".
func walkSelect(sel ast.SelectExpr, bound map[string]int, visit func(string)) {
	operand := sel.Operand()

	if operand.Kind() == ast.IdentKind && operand.AsIdent() == VarSteps && bound[VarSteps] == 0 {
		visit(VarSteps + "." + sel.FieldName())

		return
	}

	walk(operand, bound, visit)
}

// walkComprehension walks a comprehension with its variables bound in
// the loop and the result.
func walkComprehension(c ast.ComprehensionExpr, bound map[string]int, visit func(string)) {
	walk(c.IterRange(), bound, visit)
	walk(c.AccuInit(), bound, visit)

	vars := []string{c.IterVar(), c.AccuVar()}
	if c.HasIterVar2() {
		vars = append(vars, c.IterVar2())
	}

	for _, v := range vars {
		bound[v]++
	}

	walk(c.LoopCondition(), bound, visit)
	walk(c.LoopStep(), bound, visit)
	walk(c.Result(), bound, visit)

	for _, v := range vars {
		bound[v]--
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
