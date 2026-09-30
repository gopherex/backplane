package bindings

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// Value is a value built by CEL: a JSON tree (google.protobuf.Value) whose
// strings are CEL expressions, objects are built field by field, lists
// item by item, and numbers, booleans and null are literals. Zero: unset,
// the default of its place. A Value is immutable: its tree is never
// changed after construction.
type Value struct{ v *structpb.Value }

// ValueKind is what a Value node is.
type ValueKind int

// Kinds of Value nodes.
const (
	// ValueUnset: the zero Value.
	ValueUnset ValueKind = iota
	// ValueExpr: a CEL expression (a JSON string).
	ValueExpr
	// ValueObject: an object built field by field.
	ValueObject
	// ValueList: a list built item by item.
	ValueList
	// ValueLiteral: a number, a boolean or null.
	ValueLiteral
)

// ValueOf is the Value of a message; nil: unset. The message is copied.
func ValueOf(m *structpb.Value) Value {
	if m == nil || m.GetKind() == nil {
		return Value{}
	}

	c, ok := proto.Clone(m).(*structpb.Value)
	if !ok {
		return Value{}
	}

	return Value{v: c}
}

// Expr is a Value of one expression.
func Expr(src string) Value { return Value{v: structpb.NewStringValue(src)} }

// ParseValue reads a Value from its JSON form; empty or "null" text is
// unset.
func ParseValue(data []byte) (Value, error) {
	if len(data) == 0 || string(data) == "null" {
		return Value{}, nil
	}

	var m structpb.Value
	if err := protojson.Unmarshal(data, &m); err != nil {
		return Value{}, fmt.Errorf("bindings: value: %w", err)
	}

	return Value{v: &m}, nil
}

// JSON is the Value's JSON form; nil when unset.
func (v Value) JSON() []byte {
	if v.v == nil {
		return nil
	}

	out, err := protojson.Marshal(v.v)
	if err != nil {
		return nil
	}

	return out
}

// PB is the Value as a message (a copy); nil when unset.
func (v Value) PB() *structpb.Value {
	if v.v == nil {
		return nil
	}

	c, _ := proto.Clone(v.v).(*structpb.Value)

	return c
}

// IsZero reports whether the value is unset.
func (v Value) IsZero() bool { return v.v == nil }

// Equal reports whether two values say the same.
func (v Value) Equal(o Value) bool { return proto.Equal(v.v, o.v) }

// Kind is what the node is.
func (v Value) Kind() ValueKind {
	switch v.v.GetKind().(type) {
	case nil:
		return ValueUnset
	case *structpb.Value_StringValue:
		return ValueExpr
	case *structpb.Value_StructValue:
		return ValueObject
	case *structpb.Value_ListValue:
		return ValueList
	default:
		return ValueLiteral
	}
}

// Expr is the expression of a ValueExpr node.
func (v Value) Expr() string { return v.v.GetStringValue() }

// Field is one field of an object Value.
type Field struct {
	Name  string
	Value Value
}

// Fields are an object node's fields by name.
func (v Value) Fields() []Field {
	m := v.v.GetStructValue().GetFields()
	out := make([]Field, 0, len(m))

	for _, name := range sortedKeys(m) {
		out = append(out, Field{Name: name, Value: Value{v: m[name]}})
	}

	return out
}

// Items are a list node's items.
func (v Value) Items() []Value {
	list := v.v.GetListValue().GetValues()
	out := make([]Value, 0, len(list))

	for _, item := range list {
		out = append(out, Value{v: item})
	}

	return out
}

// Literal is a literal node as JSON decodes it: float64, bool or nil.
func (v Value) Literal() any {
	switch x := v.v.GetKind().(type) {
	case *structpb.Value_NumberValue:
		return x.NumberValue
	case *structpb.Value_BoolValue:
		return x.BoolValue
	default:
		return nil
	}
}

// Expressions are the value's expressions with their JSON Pointers under
// base, in path order.
func (v Value) Expressions(base string) []PathExpr {
	var out []PathExpr

	v.walkExprs(base, func(path, src string) { out = append(out, PathExpr{Path: path, Expr: src}) })

	return out
}

// PathExpr is an expression and where it is.
type PathExpr struct {
	Path string
	Expr string
}

func (v Value) walkExprs(path string, visit func(path, src string)) {
	switch v.Kind() {
	case ValueExpr:
		visit(path, v.Expr())
	case ValueObject:
		for _, f := range v.Fields() {
			f.Value.walkExprs(path+"/"+escapePointer(f.Name), visit)
		}
	case ValueList:
		for i, item := range v.Items() {
			item.walkExprs(path+"/"+strconv.Itoa(i), visit)
		}
	case ValueUnset, ValueLiteral:
	}
}

// mapExprs is the value with every expression replaced by fn(path, src).
func (v Value) mapExprs(path string, fn func(path, src string) string) Value {
	if v.IsZero() {
		return v
	}

	return Value{v: mapNode(v.v, path, fn)}
}

func mapNode(n *structpb.Value, path string, fn func(path, src string) string) *structpb.Value {
	switch x := n.GetKind().(type) {
	case *structpb.Value_StringValue:
		return structpb.NewStringValue(fn(path, x.StringValue))
	case *structpb.Value_StructValue:
		fields := make(map[string]*structpb.Value, len(x.StructValue.GetFields()))
		for name, f := range x.StructValue.GetFields() {
			fields[name] = mapNode(f, path+"/"+escapePointer(name), fn)
		}

		return structpb.NewStructValue(&structpb.Struct{Fields: fields})
	case *structpb.Value_ListValue:
		items := make([]*structpb.Value, 0, len(x.ListValue.GetValues()))
		for i, item := range x.ListValue.GetValues() {
			items = append(items, mapNode(item, path+"/"+strconv.Itoa(i), fn))
		}

		return structpb.NewListValue(&structpb.ListValue{Values: items})
	default:
		c, _ := proto.Clone(n).(*structpb.Value)

		return c
	}
}

// escapePointer escapes a key for a JSON Pointer (RFC 6901).
func escapePointer(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Pointer joins keys into a JSON Pointer.
func Pointer(keys ...string) string {
	var b strings.Builder

	for _, k := range keys {
		b.WriteByte('/')
		b.WriteString(escapePointer(k))
	}

	return b.String()
}

// sortSteps orders steps by name.
func sortSteps(steps []Step) []Step {
	slices.SortFunc(steps, func(a, b Step) int { return strings.Compare(a.Name, b.Name) })

	return steps
}
