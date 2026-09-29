package audit

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/store"
)

const (
	resultDeadline   = 5 * time.Second
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
)

// Commands wraps only the enumerated external-control RPCs. Database mutations
// append their audit inside the actual mutation transaction instead.
func (s *Service) Commands(
	register func(grpc.ServiceRegistrar), author func(context.Context) string,
) func(grpc.ServiceRegistrar) {
	return func(next grpc.ServiceRegistrar) { register(commandRegistrar{next: next, service: s, author: author}) }
}

type commandRegistrar struct {
	next    grpc.ServiceRegistrar
	service *Service
	author  func(context.Context) string
}

func (r commandRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	wrapped := *desc

	wrapped.Methods = append([]grpc.MethodDesc(nil), desc.Methods...)
	for i, method := range wrapped.Methods {
		action := commandAction("/" + desc.ServiceName + "/" + method.MethodName)
		if action == "" {
			continue
		}

		wrapped.Methods[i].Handler = func(
			server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor,
		) (any, error) {
			return method.Handler(server, ctx, decode, func(
				ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
			) (any, error) {
				audited := func(ctx context.Context, req any) (any, error) {
					return r.service.command(ctx, r.author(ctx), action, req, next)
				}
				if interceptor != nil {
					return interceptor(ctx, req, info, audited)
				}

				return audited(ctx, req)
			})
		}
	}

	r.next.RegisterService(&wrapped, impl)
}

func commandAction(method string) string {
	actions := map[string]string{
		consolev1.EventService_PublishTestEvent_FullMethodName:   "event.publish_test",
		consolev1.EventService_RedriveDeadLetters_FullMethodName: "event.redrive",
		consolev1.EventService_PurgeDeadLetters_FullMethodName:   "event.purge",
		consolev1.WorkflowService_StartWorkflow_FullMethodName:   "workflow.start",
		consolev1.WorkflowService_CancelRun_FullMethodName:       "workflow.cancel",
		consolev1.WorkflowService_TerminateRun_FullMethodName:    "workflow.terminate",
		consolev1.WorkflowService_SignalRun_FullMethodName:       "workflow.signal",
		consolev1.ScheduleService_PauseSchedule_FullMethodName:   "schedule.pause",
		consolev1.ScheduleService_UnpauseSchedule_FullMethodName: "schedule.resume",
		consolev1.ScheduleService_TriggerSchedule_FullMethodName: "schedule.trigger",
		consolev1.CallService_CallHook_FullMethodName:            "hook.call",
		consolev1.CallService_RunActivity_FullMethodName:         "activity.run",
		consolev1.BindingService_TestBinding_FullMethodName:      "binding.test",
		consolev1.BindingService_CancelBindingRun_FullMethodName: "binding.cancel",
		consolev1.RuleService_TestRule_FullMethodName:            "rule.test",
		consolev1.RuleService_CancelRuleRun_FullMethodName:       "rule.cancel",
	}

	return actions[method]
}

func (s *Service) command(ctx context.Context, actor, action string, req any, next grpc.UnaryHandler) (any, error) {
	draft := store.AuditDraft{
		Actor:       actor,
		Action:      action,
		Subject:     commandSubject(req),
		Outcome:     "intent",
		OperationID: uuid.New(),
	}
	if _, err := s.store.Get().AppendAudit(ctx, draft); err != nil {
		return nil, rpcError(codes.Unavailable, "cannot persist audit intent; command was not dispatched")
	}

	response, callErr := next(ctx, req)
	draft.Outcome, draft.Detail = commandResult(response, callErr)

	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultDeadline)
	defer cancel()

	if _, err := s.store.Get().AppendAudit(resultCtx, draft); err != nil {
		return nil, rpcError(codes.Aborted, "command may have completed; audit operation "+
			draft.OperationID.String()+" has an unresolved result")
	}

	return response, callErr
}

func commandSubject(value any) string {
	message, isMessage := value.(proto.Message)
	if !isMessage {
		return ""
	}

	var parts []string

	for _, name := range []protoreflect.Name{
		"service",
		"hook",
		"activity",
		"workflow",
		"subscriber",
		"consumer",
		"name",
		"id",
		"workflow_id",
		"run_id",
	} {
		if field := message.ProtoReflect().Descriptor().Fields().ByName(name); field != nil &&
			field.Kind() == protoreflect.StringKind && !field.IsList() {
			if text := message.ProtoReflect().Get(field).String(); text != "" {
				parts = append(parts, string(name)+"="+text)
			}
		}
	}

	// Only PublishTestEvent.event is an identifier; TestRule.event is a payload.
	if req, ok := value.(*consolev1.PublishTestEventRequest); ok && req.GetEvent() != "" {
		parts = append(parts, "event="+req.GetEvent())
	}

	return strings.Join(parts, ";")
}

func commandResult(response any, err error) (string, store.AuditDetail) {
	detail := store.AuditDetail{Code: status.Code(err).String()}
	if err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.PermissionDenied:
			return "rejected", detail
		default:
			return "unknown", detail
		}
	}

	return responseResult(response, detail)
}

func responseResult(response any, detail store.AuditDetail) (string, store.AuditDetail) {
	message, isMessage := response.(proto.Message)
	if !isMessage {
		return outcomeSucceeded, detail
	}

	fields := message.ProtoReflect().Descriptor().Fields()
	if field := fields.ByName("violations"); field != nil && field.IsList() &&
		message.ProtoReflect().Get(field).List().Len() > 0 {
		detail.Code = "ValidationFailed"
		return "rejected", detail
	}

	if result, ok := response.(interface{ GetResult() *consolev1.CallResult }); ok {
		call := result.GetResult()

		detail.WorkflowID, detail.RunID = call.GetWorkflowId(), call.GetRunId()
		if call.GetError() != "" {
			detail.Code = "CallFailed"
			return outcomeFailed, detail
		}
	}

	if result, ok := response.(*consolev1.TestRuleResponse); ok && result.GetError() != "" {
		detail.Code = "EvaluationFailed"
		return outcomeFailed, detail
	}

	if result, ok := response.(interface{ GetWorkflowId() string }); ok {
		detail.WorkflowID = result.GetWorkflowId()
	}

	if result, ok := response.(interface{ GetRunId() string }); ok {
		detail.RunID = result.GetRunId()
	}

	switch result := response.(type) {
	case *consolev1.RedriveDeadLettersResponse:
		return batchOutcome(result.GetRedriven(), len(result.GetFailed()), detail)
	case *consolev1.PurgeDeadLettersResponse:
		return batchOutcome(result.GetPurged(), len(result.GetFailed()), detail)
	default:
		return outcomeSucceeded, detail
	}
}

func batchOutcome(completed uint64, failed int, detail store.AuditDetail) (string, store.AuditDetail) {
	if failed == 0 {
		return outcomeSucceeded, detail
	}

	detail.Code = "BatchFailed"
	if completed == 0 {
		return outcomeFailed, detail
	}

	return "partial", detail
}
