package bindings

import (
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
)

// BindingFromPB is the definition a message carries.
func BindingFromPB(m *consolev1.BindingDefinition) Binding {
	return Binding{Hook: m.GetHook(), Steps: stepsFromPB(m.GetSteps()), Result: valueFromPB(m.GetResult())}
}

// PB is the definition as a message.
func (b Binding) PB() *consolev1.BindingDefinition {
	return &consolev1.BindingDefinition{Hook: b.Hook, Steps: stepsPB(b.Steps), Result: b.Result.pb()}
}

// RuleFromPB is the definition a message carries.
func RuleFromPB(m *consolev1.RuleDefinition) Rule {
	return Rule{Event: m.GetEvent(), When: m.GetWhen(), Steps: stepsFromPB(m.GetSteps())}
}

// PB is the definition as a message.
func (r Rule) PB() *consolev1.RuleDefinition {
	return &consolev1.RuleDefinition{Event: r.Event, When: r.When, Steps: stepsPB(r.Steps)}
}

func stepsFromPB(in []*consolev1.BindingStep) []Step {
	out := make([]Step, 0, len(in))

	for _, s := range in {
		out = append(out, Step{
			Name: s.GetName(), Activity: s.GetActivity(), Input: valueFromPB(s.GetInput()), When: s.GetWhen(),
			After: append([]string(nil), s.GetAfter()...), Undo: s.GetUndo(), UndoInput: valueFromPB(s.GetUndoInput()),
			Retry: Retry{
				Attempts:        int(min(s.GetRetry().GetAttempts(), math.MaxInt32)),
				InitialInterval: durationOf(s.GetRetry().GetInitialInterval()),
				MaxInterval:     durationOf(s.GetRetry().GetMaxInterval()),
				Backoff:         s.GetRetry().GetBackoff(),
			},
			StartToClose: durationOf(s.GetStartToClose()), Heartbeat: durationOf(s.GetHeartbeat()),
		})
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func stepsPB(in []Step) []*consolev1.BindingStep {
	out := make([]*consolev1.BindingStep, 0, len(in))

	for i := range in {
		s := &in[i]
		m := &consolev1.BindingStep{
			Name: s.Name, Activity: s.Activity, Input: s.Input.pb(), When: s.When,
			After: append([]string(nil), s.After...), Undo: s.Undo, UndoInput: s.UndoInput.pb(),
			StartToClose: durationPB(s.StartToClose), Heartbeat: durationPB(s.Heartbeat),
		}

		if !s.Retry.IsZero() {
			m.Retry = &consolev1.BindingRetry{
				Attempts:        uint32(min(max(s.Retry.Attempts, 0), math.MaxInt32)), //nolint:gosec // bounded
				InitialInterval: durationPB(s.Retry.InitialInterval),
				MaxInterval:     durationPB(s.Retry.MaxInterval),
				Backoff:         s.Retry.Backoff,
			}
		}

		out = append(out, m)
	}

	return out
}

func valueFromPB(m *consolev1.BindingValue) Value {
	v := Value{Expr: m.GetExpr()}

	for _, f := range m.GetFields() {
		v.Fields = append(v.Fields, Field{Name: f.GetName(), Expr: f.GetExpr()})
	}

	return v
}

func (v Value) pb() *consolev1.BindingValue {
	if v.IsZero() {
		return nil
	}

	m := &consolev1.BindingValue{Expr: v.Expr}
	for _, f := range v.Fields {
		m.Fields = append(m.Fields, &consolev1.BindingField{Name: f.Name, Expr: f.Expr})
	}

	return m
}

func durationOf(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}

	return d.AsDuration()
}

func durationPB(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}

	return durationpb.New(d)
}

// PB is the version as a message.
func (v BindingVersion) PB() *consolev1.BindingVersion {
	out := &consolev1.BindingVersion{
		Hook: v.Hook, Version: uint64(max(v.Version, 0)), Deleted: v.Deleted, Author: v.Author, Comment: v.Comment,
		CreatedAt: timestamppb.New(v.CreatedAt), RollbackOf: uint64(max(v.RollbackOf, 0)),
	}
	if !v.Deleted {
		out.Definition = v.Binding.PB()
	}

	return out
}

// PB is the version as a message.
func (v RuleVersion) PB() *consolev1.RuleVersion {
	out := &consolev1.RuleVersion{
		RuleId: v.RuleID.String(), Version: uint64(max(v.Version, 0)), Name: v.Name, Deleted: v.Deleted,
		Author: v.Author, Comment: v.Comment, CreatedAt: timestamppb.New(v.CreatedAt),
		RollbackOf: uint64(max(v.RollbackOf, 0)),
	}
	if !v.Deleted {
		out.Definition = v.Rule.PB()
	}

	return out
}

// PB is the rule as a message.
func (r RuleEntry) PB() *consolev1.Rule {
	return &consolev1.Rule{
		Id: r.ID.String(), Paused: r.Paused, Current: r.Current.PB(), CreatedAt: timestamppb.New(r.CreatedAt),
	}
}

// ViolationsPB are the violations as messages.
func ViolationsPB(vs []Violation) []*consolev1.BindingViolation {
	out := make([]*consolev1.BindingViolation, 0, len(vs))
	for _, v := range vs {
		out = append(out, &consolev1.BindingViolation{Path: v.Path, Code: v.Code, Message: v.Message})
	}

	return out
}

// encodeBinding is the stored form of a definition: protojson.
func encodeBinding(b Binding) ([]byte, error) {
	out, err := protojson.Marshal(b.PB())
	if err != nil {
		return nil, fmt.Errorf("bindings: encode %s: %w", b.Hook, err)
	}

	return out, nil
}

func decodeBinding(data []byte) (Binding, error) {
	var m consolev1.BindingDefinition
	if err := protojson.Unmarshal(data, &m); err != nil {
		return Binding{}, fmt.Errorf("bindings: decode: %w", err)
	}

	return BindingFromPB(&m), nil
}

func encodeRule(r Rule) ([]byte, error) {
	out, err := protojson.Marshal(r.PB())
	if err != nil {
		return nil, fmt.Errorf("bindings: encode rule: %w", err)
	}

	return out, nil
}

func decodeRule(data []byte) (Rule, error) {
	var m consolev1.RuleDefinition
	if err := protojson.Unmarshal(data, &m); err != nil {
		return Rule{}, fmt.Errorf("bindings: decode rule: %w", err)
	}

	return RuleFromPB(&m), nil
}
