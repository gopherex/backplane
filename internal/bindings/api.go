package bindings

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/registry"
)

// Page sizes of the version lists.
const (
	defaultPage = 50
	maxPage     = 500
)

// invalid is the InvalidArgument status of a request.
func invalid(msg string) error {
	return status.Error(codes.InvalidArgument, msg) //nolint:wrapcheck // a gRPC status travels as is
}

// Register registers BindingService and RuleService on r.
func (m *Manager) Register(r grpc.ServiceRegistrar) {
	consolev1.RegisterBindingServiceServer(r, m.BindingAPI())
	consolev1.RegisterRuleServiceServer(r, m.RuleAPI())
}

// status maps errors to gRPC codes; unexpected ones are logged.
func (m *Manager) status(err error) error {
	code := codes.Internal

	switch {
	case errors.Is(err, ErrNotSynced):
		code = codes.Unavailable
	case errors.Is(err, ErrNoBinding), errors.Is(err, ErrNoVersion), errors.Is(err, ErrNoRule):
		code = codes.NotFound
	case errors.Is(err, ErrDeleted):
		code = codes.FailedPrecondition
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		m.Log().Error("bindings api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

func pageSize(n uint32) int {
	size := int(n)
	if size <= 0 {
		size = defaultPage
	}

	return min(size, maxPage)
}

func toInt64(v uint64) int64 { return int64(min(v, math.MaxInt64)) } //nolint:gosec // bounded

// ---- BindingService ---------------------------------------------------------

// BindingAPI is the Manager as backplane.console.v1.BindingService.
type BindingAPI struct {
	consolev1.UnimplementedBindingServiceServer

	m *Manager
}

var _ consolev1.BindingServiceServer = BindingAPI{}

// BindingAPI serves m's bindings to the console.
func (m *Manager) BindingAPI() BindingAPI { return BindingAPI{m: m} }

// ListBindings implements BindingService.
func (a BindingAPI) ListBindings(
	ctx context.Context, req *consolev1.ListBindingsRequest,
) (*consolev1.ListBindingsResponse, error) {
	list, err := a.m.HookBindings(ctx, req.GetService())
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.ListBindingsResponse{Bindings: list}, nil
}

// HookBindings is every hook of the latest manifests (of one service, or
// all for "") with its binding, and every binding whose hook is no longer
// declared, by hook.
func (m *Manager) HookBindings(ctx context.Context, service string) ([]*consolev1.HookBinding, error) {
	cat := m.src.Current()
	if cat.Index == 0 {
		return nil, ErrNotSynced
	}

	current, err := m.CurrentAll(ctx)
	if err != nil {
		return nil, err
	}

	byHook := declaredHooks(cat, service)

	for i := range current {
		v := &current[i]

		svc, _ := SplitName(v.Hook)
		if service != "" && svc != service {
			continue
		}

		binding := byHook[v.Hook]
		if binding == nil {
			binding = &consolev1.HookBinding{Hook: v.Hook, Service: svc}
			byHook[v.Hook] = binding
		}

		binding.Current = v.PB()
	}

	out := make([]*consolev1.HookBinding, 0, len(byHook))

	for _, name := range sortedKeys(byHook) {
		binding := byHook[name]
		binding.State = bindingState(binding)
		out = append(out, binding)
	}

	return out, nil
}

// declaredHooks is every hook of the latest manifests (of one service, or
// all for ""), unbound, by hook.
func declaredHooks(cat registry.Catalog, service string) map[string]*consolev1.HookBinding {
	byHook := map[string]*consolev1.HookBinding{}

	for _, name := range sortedKeys(cat.Services) {
		if service != "" && name != service {
			continue
		}

		for _, h := range latestHooks(cat.Services[name]) {
			full := name + "." + h.GetName()
			byHook[full] = &consolev1.HookBinding{
				Hook: full, Service: name, Declared: true, Required: h.GetRequired(), Description: h.GetDescription(),
			}
		}
	}

	return byHook
}

// bindingState is whether a hook is bound, and if not whether it must be.
func bindingState(binding *consolev1.HookBinding) consolev1.BindingState {
	switch {
	case binding.GetCurrent() != nil && !binding.GetCurrent().GetDeleted():
		return consolev1.BindingState_BINDING_STATE_BOUND
	case binding.GetRequired():
		return consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND
	default:
		return consolev1.BindingState_BINDING_STATE_UNBOUND
	}
}

func latestHooks(s registry.Service) []*backplanev1.Hook { return s.Latest().GetHooks() }

// GetBinding implements BindingService.
func (a BindingAPI) GetBinding(
	ctx context.Context, req *consolev1.GetBindingRequest,
) (*consolev1.GetBindingResponse, error) {
	if req.GetHook() == "" {
		return nil, invalid("hook is required")
	}

	v, err := a.m.Version(ctx, req.GetHook(), toInt64(req.GetVersion()))
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.GetBindingResponse{Version: v.PB()}, nil
}

// ListBindingVersions implements BindingService.
func (a BindingAPI) ListBindingVersions(
	ctx context.Context, req *consolev1.ListBindingVersionsRequest,
) (*consolev1.ListBindingVersionsResponse, error) {
	if req.GetHook() == "" {
		return nil, invalid("hook is required")
	}

	size := pageSize(req.GetPageSize())

	vs, err := a.m.Versions(ctx, req.GetHook(), toInt64(req.GetBefore()), size+1)
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.ListBindingVersionsResponse{}

	if len(vs) > size {
		vs = vs[:size]
		out.NextBefore = uint64(vs[size-1].Version) //nolint:gosec // versions are positive
	}

	for i := range vs {
		out.Versions = append(out.Versions, vs[i].PB())
	}

	return out, nil
}

// ValidateBinding implements BindingService.
func (a BindingAPI) ValidateBinding(
	_ context.Context, req *consolev1.ValidateBindingRequest,
) (*consolev1.ValidateBindingResponse, error) {
	vs, err := a.m.Validate(BindingFromPB(req.GetDefinition()))
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.ValidateBindingResponse{Violations: ViolationsPB(vs)}, nil
}

// SaveBinding implements BindingService.
func (a BindingAPI) SaveBinding(
	ctx context.Context, req *consolev1.SaveBindingRequest,
) (*consolev1.SaveBindingResponse, error) {
	saved, err := a.m.Save(ctx, BindingFromPB(req.GetDefinition()), req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.SaveBindingResponse{Violations: ViolationsPB(saved.Violations)}
	if len(saved.Violations) == 0 {
		out.Version = saved.Version.PB()
	}

	return out, nil
}

// RollbackBinding implements BindingService.
func (a BindingAPI) RollbackBinding(
	ctx context.Context, req *consolev1.RollbackBindingRequest,
) (*consolev1.RollbackBindingResponse, error) {
	if req.GetHook() == "" || req.GetVersion() == 0 {
		return nil, invalid("hook and version are required")
	}

	saved, err := a.m.Rollback(ctx, req.GetHook(), toInt64(req.GetVersion()), req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.RollbackBindingResponse{Violations: ViolationsPB(saved.Violations)}
	if len(saved.Violations) == 0 {
		out.Version = saved.Version.PB()
	}

	return out, nil
}

// DeleteBinding implements BindingService.
func (a BindingAPI) DeleteBinding(
	ctx context.Context, req *consolev1.DeleteBindingRequest,
) (*consolev1.DeleteBindingResponse, error) {
	if req.GetHook() == "" {
		return nil, invalid("hook is required")
	}

	v, err := a.m.Delete(ctx, req.GetHook(), req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.DeleteBindingResponse{Version: v.PB()}, nil
}

// ParseBinding implements BindingService.
func (a BindingAPI) ParseBinding(
	_ context.Context, req *consolev1.ParseBindingRequest,
) (*consolev1.ParseBindingResponse, error) {
	b, err := ParseBinding(req.GetText())
	if err != nil {
		return &consolev1.ParseBindingResponse{Errors: parseErrorsPB(err)}, nil
	}

	return &consolev1.ParseBindingResponse{Definition: b.PB()}, nil
}

// FormatBinding implements BindingService.
func (a BindingAPI) FormatBinding(
	_ context.Context, req *consolev1.FormatBindingRequest,
) (*consolev1.FormatBindingResponse, error) {
	return &consolev1.FormatBindingResponse{Text: FormatBinding(BindingFromPB(req.GetDefinition()))}, nil
}

// WatchBindings implements BindingService: the list now, then again
// whenever it differs — a save here or on another replica, a manifest.
func (a BindingAPI) WatchBindings(
	req *consolev1.WatchBindingsRequest, stream grpc.ServerStreamingServer[consolev1.WatchBindingsResponse],
) error {
	ctx := stream.Context()

	return follow(ctx, a.m, func() (*consolev1.WatchBindingsResponse, error) {
		list, err := a.m.HookBindings(ctx, req.GetService())

		return &consolev1.WatchBindingsResponse{Bindings: list}, err
	}, stream.Send)
}

// follow sends view now and after every change of the registry or the
// store, when it differs from the last one sent.
func follow[M proto.Message](
	ctx context.Context, m *Manager, view func() (M, error), send func(M) error,
) error {
	reg := m.src.Changes(ctx)
	saves := m.changes.subscribe(ctx)

	var (
		last    M
		hasLast bool
	)

	for {
		msg, err := view()
		if err != nil && !errors.Is(err, ErrNotSynced) {
			return m.status(err)
		}

		if err == nil && (!hasLast || !proto.Equal(msg, last)) {
			if err := send(msg); err != nil {
				return err
			}

			last, hasLast = msg, true
		}

		select {
		case _, ok := <-reg:
			if !ok {
				return nil
			}
		case _, ok := <-saves:
			if !ok {
				return nil
			}
		}
	}
}

func parseErrorsPB(err error) []*consolev1.BindingParseError {
	var pes ParseErrors
	if !errors.As(err, &pes) {
		return []*consolev1.BindingParseError{{Message: err.Error()}}
	}

	out := make([]*consolev1.BindingParseError, 0, len(pes))
	for _, e := range pes {
		out = append(out, &consolev1.BindingParseError{
			Line: uint32(max(e.Line, 0)), Column: uint32(max(e.Column, 0)), Message: e.Message, //nolint:gosec // small
		})
	}

	return out
}

// ---- RuleService ------------------------------------------------------------

// RuleAPI is the Manager as backplane.console.v1.RuleService.
type RuleAPI struct {
	consolev1.UnimplementedRuleServiceServer

	m *Manager
}

var _ consolev1.RuleServiceServer = RuleAPI{}

// RuleAPI serves m's rules to the console.
func (m *Manager) RuleAPI() RuleAPI { return RuleAPI{m: m} }

func ruleID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, invalid("id is not a uuid")
	}

	return id, nil
}

func (m *Manager) rulesPB(ctx context.Context, event string) ([]*consolev1.Rule, error) {
	rules, err := m.Rules(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*consolev1.Rule, 0, len(rules))

	for i := range rules {
		if event != "" && rules[i].Current.Rule.Event != event {
			continue
		}

		out = append(out, rules[i].PB())
	}

	return out, nil
}

// ListRules implements RuleService.
func (a RuleAPI) ListRules(ctx context.Context, req *consolev1.ListRulesRequest) (*consolev1.ListRulesResponse, error) {
	rules, err := a.m.rulesPB(ctx, req.GetEvent())
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.ListRulesResponse{Rules: rules}, nil
}

// GetRule implements RuleService.
func (a RuleAPI) GetRule(ctx context.Context, req *consolev1.GetRuleRequest) (*consolev1.GetRuleResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	e, err := a.m.Rule(ctx, id)
	if err != nil {
		return nil, a.m.status(err)
	}

	v := e.Current
	if req.GetVersion() != 0 {
		if v, err = a.m.RuleVersion(ctx, id, toInt64(req.GetVersion())); err != nil {
			return nil, a.m.status(err)
		}
	}

	return &consolev1.GetRuleResponse{Rule: e.PB(), Version: v.PB()}, nil
}

// ListRuleVersions implements RuleService.
func (a RuleAPI) ListRuleVersions(
	ctx context.Context, req *consolev1.ListRuleVersionsRequest,
) (*consolev1.ListRuleVersionsResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	size := pageSize(req.GetPageSize())

	vs, err := a.m.RuleVersions(ctx, id, toInt64(req.GetBefore()), size+1)
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.ListRuleVersionsResponse{}

	if len(vs) > size {
		vs = vs[:size]
		out.NextBefore = uint64(vs[size-1].Version) //nolint:gosec // versions are positive
	}

	for i := range vs {
		out.Versions = append(out.Versions, vs[i].PB())
	}

	return out, nil
}

// ValidateRule implements RuleService (the name is not checked: it is not
// part of the definition).
func (a RuleAPI) ValidateRule(
	_ context.Context, req *consolev1.ValidateRuleRequest,
) (*consolev1.ValidateRuleResponse, error) {
	cat, err := a.m.Catalog()
	if err != nil {
		return nil, a.m.status(err)
	}

	vs, err := ValidateRule(RuleFromPB(req.GetDefinition()), cat)
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.ValidateRuleResponse{Violations: ViolationsPB(vs)}, nil
}

// SaveRule implements RuleService.
func (a RuleAPI) SaveRule(ctx context.Context, req *consolev1.SaveRuleRequest) (*consolev1.SaveRuleResponse, error) {
	id := uuid.Nil

	if req.GetId() != "" {
		var err error
		if id, err = ruleID(req.GetId()); err != nil {
			return nil, err
		}
	}

	saved, err := a.m.SaveRule(ctx, id, req.GetName(), RuleFromPB(req.GetDefinition()), req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.SaveRuleResponse{Violations: ViolationsPB(saved.Violations)}
	if len(saved.Violations) == 0 {
		out.Version = saved.Version.PB()
	}

	return out, nil
}

// RollbackRule implements RuleService.
func (a RuleAPI) RollbackRule(
	ctx context.Context, req *consolev1.RollbackRuleRequest,
) (*consolev1.RollbackRuleResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	if req.GetVersion() == 0 {
		return nil, invalid("version is required")
	}

	saved, err := a.m.RollbackRule(ctx, id, toInt64(req.GetVersion()), req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	out := &consolev1.RollbackRuleResponse{Violations: ViolationsPB(saved.Violations)}
	if len(saved.Violations) == 0 {
		out.Version = saved.Version.PB()
	}

	return out, nil
}

// DeleteRule implements RuleService.
func (a RuleAPI) DeleteRule(
	ctx context.Context, req *consolev1.DeleteRuleRequest,
) (*consolev1.DeleteRuleResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	v, err := a.m.DeleteRule(ctx, id, req.GetComment())
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.DeleteRuleResponse{Version: v.PB()}, nil
}

// PauseRule implements RuleService.
func (a RuleAPI) PauseRule(ctx context.Context, req *consolev1.PauseRuleRequest) (*consolev1.PauseRuleResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	e, err := a.m.PauseRule(ctx, id, true)
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.PauseRuleResponse{Rule: e.PB()}, nil
}

// ResumeRule implements RuleService.
func (a RuleAPI) ResumeRule(
	ctx context.Context, req *consolev1.ResumeRuleRequest,
) (*consolev1.ResumeRuleResponse, error) {
	id, err := ruleID(req.GetId())
	if err != nil {
		return nil, err
	}

	e, err := a.m.PauseRule(ctx, id, false)
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.ResumeRuleResponse{Rule: e.PB()}, nil
}

// ParseRule implements RuleService.
func (a RuleAPI) ParseRule(_ context.Context, req *consolev1.ParseRuleRequest) (*consolev1.ParseRuleResponse, error) {
	r, err := ParseRule(req.GetText())
	if err != nil {
		return &consolev1.ParseRuleResponse{Errors: parseErrorsPB(err)}, nil
	}

	return &consolev1.ParseRuleResponse{Definition: r.PB()}, nil
}

// FormatRule implements RuleService.
func (a RuleAPI) FormatRule(
	_ context.Context, req *consolev1.FormatRuleRequest,
) (*consolev1.FormatRuleResponse, error) {
	return &consolev1.FormatRuleResponse{Text: FormatRule(RuleFromPB(req.GetDefinition()))}, nil
}

// WatchRules implements RuleService: the list now, then again whenever it
// differs.
func (a RuleAPI) WatchRules(
	req *consolev1.WatchRulesRequest, stream grpc.ServerStreamingServer[consolev1.WatchRulesResponse],
) error {
	ctx := stream.Context()

	return follow(ctx, a.m, func() (*consolev1.WatchRulesResponse, error) {
		rules, err := a.m.rulesPB(ctx, req.GetEvent())

		return &consolev1.WatchRulesResponse{Rules: rules}, err
	}, stream.Send)
}
