package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/gopherex/xlog"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/wire"
)

// maxHistory bounds the history events GetRun returns.
const maxHistory = 1000

// WorkflowAPI is backplane.console.v1.WorkflowService.
type WorkflowAPI struct {
	consolev1.UnimplementedWorkflowServiceServer

	o *Ops
}

var _ consolev1.WorkflowServiceServer = WorkflowAPI{}

// workflowDefs are the workflows of the manifests: workflows[] and the
// activities of kind WORKFLOW.
func workflowDefs(ms []*backplanev1.Manifest) []*consolev1.WorkflowDef {
	var out []*consolev1.WorkflowDef

	for _, m := range ms {
		for _, w := range m.GetWorkflows() {
			out = append(out, &consolev1.WorkflowDef{
				Service: m.GetService(), Name: w.GetName(), Kind: consolev1.WorkflowKind_WORKFLOW_KIND_WORKFLOW,
				Description: w.GetDescription(), Input: w.GetInput(), Output: w.GetOutput(),
				TaskQueue: wire.Queue(m.GetService()),
			})
		}

		for _, a := range m.GetActivities() {
			if a.GetKind() != backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
				continue
			}

			out = append(out, &consolev1.WorkflowDef{
				Service: m.GetService(), Name: a.GetName(), Kind: consolev1.WorkflowKind_WORKFLOW_KIND_ACTIVITY,
				Description: a.GetDescription(), Input: a.GetInput(), Output: a.GetOutput(),
				TaskQueue: wire.Queue(m.GetService()),
			})
		}
	}

	return out
}

// ListWorkflows implements WorkflowService.
func (a WorkflowAPI) ListWorkflows(
	ctx context.Context, req *consolev1.ListWorkflowsRequest,
) (*consolev1.ListWorkflowsResponse, error) {
	ms, err := a.o.manifests(req.GetService())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	return &consolev1.ListWorkflowsResponse{Workflows: workflowDefs(ms)}, nil
}

// StartWorkflow implements WorkflowService.
func (a WorkflowAPI) StartWorkflow(
	ctx context.Context, req *consolev1.StartWorkflowRequest,
) (*consolev1.StartWorkflowResponse, error) {
	opts, args, err := a.start(ctx, req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	run, err := c.ExecuteWorkflow(ctx, opts, req.GetWorkflow(), args...)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	a.o.audit(ctx, "workflow.start", req.GetService()+"."+req.GetWorkflow(),
		xlog.String("workflow_id", run.GetID()), xlog.String("run_id", run.GetRunID()))

	return &consolev1.StartWorkflowResponse{WorkflowId: run.GetID(), RunId: run.GetRunID()}, nil
}

// start is what StartWorkflow starts: the options and the argument, the
// JSON input as is or wrapped in ActivityCall for an activity.
func (a WorkflowAPI) start(
	ctx context.Context, req *consolev1.StartWorkflowRequest,
) (client.StartWorkflowOptions, []any, error) {
	var opts client.StartWorkflowOptions

	if !serviceName.MatchString(req.GetService()) || req.GetWorkflow() == "" {
		return opts, nil, fmt.Errorf("%w: service and workflow are required", ErrInput)
	}

	m, err := a.o.latest(req.GetService())
	if err != nil {
		return opts, nil, err
	}

	var def *consolev1.WorkflowDef

	for _, d := range workflowDefs([]*backplanev1.Manifest{m}) {
		if d.GetName() == req.GetWorkflow() {
			def = d
		}
	}

	if def == nil {
		return opts, nil, fmt.Errorf("%w: %s declares no workflow %s", ErrNotDeclared, req.GetService(), req.GetWorkflow())
	}

	input := []byte(req.GetInput())
	if len(input) > 0 && !json.Valid(input) {
		return opts, nil, fmt.Errorf("%w: input is not valid JSON", ErrInput)
	}

	var args []any

	switch {
	case def.GetKind() == consolev1.WorkflowKind_WORKFLOW_KIND_ACTIVITY:
		if len(input) == 0 {
			input = []byte("{}")
		}

		args = []any{&backplanev1.ActivityCall{
			Activity: req.GetService() + "." + req.GetWorkflow(), Payload: input, Binding: consoleBinding, Step: consoleBinding,
		}}
	case len(input) > 0:
		args = []any{json.RawMessage(input)}
	}

	id := req.GetWorkflowId()
	if id == "" {
		id = "console/" + req.GetService() + "/" + req.GetWorkflow() + "/" + uuid.NewString()
	}

	return client.StartWorkflowOptions{
		ID: id, TaskQueue: def.GetTaskQueue(), WorkflowExecutionTimeout: req.GetTimeout().AsDuration(),
		Memo: map[string]any{wire.MemoSource: a.o.author(ctx)},
	}, args, nil
}

// statusQuery is the visibility name of a status.
//
//nolint:gochecknoglobals // constant table
var statusQuery = map[consolev1.RunStatus]string{
	consolev1.RunStatus_RUN_STATUS_RUNNING:          "Running",
	consolev1.RunStatus_RUN_STATUS_COMPLETED:        "Completed",
	consolev1.RunStatus_RUN_STATUS_FAILED:           "Failed",
	consolev1.RunStatus_RUN_STATUS_CANCELED:         "Canceled",
	consolev1.RunStatus_RUN_STATUS_TERMINATED:       "Terminated",
	consolev1.RunStatus_RUN_STATUS_CONTINUED_AS_NEW: "ContinuedAsNew",
	consolev1.RunStatus_RUN_STATUS_TIMED_OUT:        "TimedOut",
}

// quote is s as a visibility string literal; quotes and backslashes are
// refused rather than escaped.
func quote(what, s string) (string, error) {
	if strings.ContainsAny(s, `'"\`) {
		return "", fmt.Errorf("%w: %s %q", ErrInput, what, s)
	}

	return "'" + s + "'", nil
}

// RunsQuery is the visibility query of ListRuns.
func RunsQuery(req *consolev1.ListRunsRequest) (string, error) {
	if !serviceName.MatchString(req.GetService()) {
		return "", fmt.Errorf("%w: service is required", ErrInput)
	}

	var parts []string

	add := func(format, what, v string) error {
		q, err := quote(what, v)
		if err != nil {
			return err
		}

		parts = append(parts, fmt.Sprintf(format, q))

		return nil
	}

	if err := add("TaskQueue = %s", "service", wire.Queue(req.GetService())); err != nil {
		return "", err
	}

	if req.GetWorkflow() != "" {
		if err := add("WorkflowType = %s", "workflow", req.GetWorkflow()); err != nil {
			return "", err
		}
	}

	if req.GetStatus() != consolev1.RunStatus_RUN_STATUS_UNSPECIFIED {
		name, ok := statusQuery[req.GetStatus()]
		if !ok {
			return "", fmt.Errorf("%w: status %v", ErrInput, req.GetStatus())
		}

		parts = append(parts, "ExecutionStatus = '"+name+"'")
	}

	if req.GetWorkflowIdPrefix() != "" {
		if err := add("WorkflowId STARTS_WITH %s", "workflow_id_prefix", req.GetWorkflowIdPrefix()); err != nil {
			return "", err
		}
	}

	// No ORDER BY: SQL visibility (the dev server, PostgreSQL, MySQL)
	// rejects it, and lists newest first anyway.
	return strings.Join(parts, " AND "), nil
}

// ListRuns implements WorkflowService.
func (a WorkflowAPI) ListRuns(
	ctx context.Context, req *consolev1.ListRunsRequest,
) (*consolev1.ListRunsResponse, error) {
	query, err := RunsQuery(req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	res, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		PageSize:      int32(pageSize(req.GetPageSize())), //nolint:gosec // bounded
		NextPageToken: req.GetPageToken(), Query: query,
	})
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.ListRunsResponse{NextPageToken: res.GetNextPageToken(), Query: query}
	for _, e := range res.GetExecutions() {
		out.Runs = append(out.Runs, runPB(e))
	}

	return out, nil
}

func runPB(e *workflowpb.WorkflowExecutionInfo) *consolev1.Run {
	out := &consolev1.Run{
		WorkflowId: e.GetExecution().GetWorkflowId(), RunId: e.GetExecution().GetRunId(),
		WorkflowType: e.GetType().GetName(), Status: consolev1.RunStatus(e.GetStatus()),
		TaskQueue: e.GetTaskQueue(), StartTime: e.GetStartTime(), CloseTime: e.GetCloseTime(),
		HistoryLength: e.GetHistoryLength(), ParentWorkflowId: e.GetParentExecution().GetWorkflowId(),
	}

	if fields := e.GetMemo().GetFields(); len(fields) > 0 {
		out.Memo = make(map[string]string, len(fields))
		for k, p := range fields {
			out.Memo[k] = payloadJSON(p)
		}
	}

	return out
}

// payloadJSON is a Temporal payload as JSON text: JSON encodings as they
// are, binary/null as null, anything else as a base64 string.
func payloadJSON(p *commonpb.Payload) string {
	data := p.GetData()

	switch enc := string(p.GetMetadata()["encoding"]); {
	case enc == "binary/null":
		return "null"
	case strings.HasPrefix(enc, "json/") && json.Valid(data):
		return string(data)
	default:
		// base64 needs no escaping: quoted, it is its JSON string.
		return `"` + base64.StdEncoding.EncodeToString(data) + `"`
	}
}

// payloadsJSON is one payload as JSON text, several as a JSON array;
// empty for none.
func payloadsJSON(ps *commonpb.Payloads) string {
	list := ps.GetPayloads()

	switch len(list) {
	case 0:
		return ""
	case 1:
		return payloadJSON(list[0])
	}

	parts := make([]string, 0, len(list))
	for _, p := range list {
		parts = append(parts, payloadJSON(p))
	}

	return "[" + strings.Join(parts, ",") + "]"
}

// GetRun implements WorkflowService.
func (a WorkflowAPI) GetRun(ctx context.Context, req *consolev1.GetRunRequest) (*consolev1.GetRunResponse, error) {
	if req.GetWorkflowId() == "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w: workflow_id is required", ErrInput))
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	desc, err := c.DescribeWorkflowExecution(ctx, req.GetWorkflowId(), req.GetRunId())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.GetRunResponse{Run: runPB(desc.GetWorkflowExecutionInfo())}
	runID := out.GetRun().GetRunId()

	for _, p := range desc.GetPendingActivities() {
		out.PendingActivities = append(out.PendingActivities, pendingPB(p))
	}

	iter := c.GetWorkflowHistory(ctx, req.GetWorkflowId(), runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	var closed bool

	for iter.HasNext() {
		e, err := iter.Next()
		if err != nil {
			return nil, a.o.status(ctx, err)
		}

		if len(out.GetHistory()) >= maxHistory {
			out.Truncated = true

			break
		}

		out.History = append(out.History, historyPB(e))
		closed = closed || outcome(out, e)
	}

	if out.GetTruncated() && out.GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_RUNNING && !closed {
		last := c.GetWorkflowHistory(ctx, req.GetWorkflowId(), runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)
		for last.HasNext() {
			e, err := last.Next()
			if err != nil {
				return nil, a.o.status(ctx, err)
			}

			outcome(out, e)
		}
	}

	return out, nil
}

// outcome records the run's input or end from e; true for an end.
func outcome(out *consolev1.GetRunResponse, e *historypb.HistoryEvent) bool {
	switch e.GetEventType() {
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
		out.Input = payloadsJSON(e.GetWorkflowExecutionStartedEventAttributes().GetInput())
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED:
		out.Result = payloadsJSON(e.GetWorkflowExecutionCompletedEventAttributes().GetResult())
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED:
		out.Failure, out.FailureType = failureOf(e.GetWorkflowExecutionFailedEventAttributes().GetFailure())
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TIMED_OUT:
		out.Failure, out.FailureType = timedOut, wire.TimeoutType
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED:
		reason := e.GetWorkflowExecutionTerminatedEventAttributes().GetReason()
		out.Failure, out.FailureType = "terminated: "+reason, "Terminated"
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED:
		out.Failure, out.FailureType = "canceled", "Canceled"
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW:
	default:
		return false
	}

	return e.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED
}

// failureOf is the deepest message of a failure chain and the type of
// its deepest application failure.
func failureOf(f *failurepb.Failure) (string, string) {
	var msg, typ string

	for ; f != nil; f = f.GetCause() {
		if f.GetMessage() != "" {
			msg = f.GetMessage()
		}

		if t := f.GetApplicationFailureInfo().GetType(); t != "" {
			typ = t
		}
	}

	return msg, typ
}

func pendingPB(p *workflowpb.PendingActivityInfo) *consolev1.PendingActivity {
	out := &consolev1.PendingActivity{
		ActivityId: p.GetActivityId(), ActivityType: p.GetActivityType().GetName(),
		State:   strings.TrimPrefix(p.GetState().String(), "PENDING_ACTIVITY_STATE_"),
		Attempt: p.GetAttempt(), MaximumAttempts: p.GetMaximumAttempts(),
		LastStartedTime: p.GetLastStartedTime(), LastHeartbeatTime: p.GetLastHeartbeatTime(),
		NextAttemptTime: p.GetNextAttemptScheduleTime(), LastWorker: p.GetLastWorkerIdentity(),
	}

	out.LastFailure, _ = failureOf(p.GetLastFailure())

	return out
}

// historyPB is one history event summarized.
//
//nolint:cyclop,funlen // one case per event kind
func historyPB(e *historypb.HistoryEvent) *consolev1.HistoryEvent {
	out := &consolev1.HistoryEvent{
		Id: e.GetEventId(), Time: e.GetEventTime(), Type: strings.TrimPrefix(e.GetEventType().String(), "EVENT_TYPE_"),
	}

	switch {
	case e.GetWorkflowExecutionStartedEventAttributes() != nil:
		at := e.GetWorkflowExecutionStartedEventAttributes()
		out.Summary = at.GetWorkflowType().GetName() + " on " + at.GetTaskQueue().GetName()
		out.Payload = payloadsJSON(at.GetInput())
	case e.GetWorkflowExecutionCompletedEventAttributes() != nil:
		out.Payload = payloadsJSON(e.GetWorkflowExecutionCompletedEventAttributes().GetResult())
	case e.GetWorkflowExecutionFailedEventAttributes() != nil:
		out.Failure, _ = failureOf(e.GetWorkflowExecutionFailedEventAttributes().GetFailure())
	case e.GetWorkflowExecutionTerminatedEventAttributes() != nil:
		out.Summary = e.GetWorkflowExecutionTerminatedEventAttributes().GetReason()
	case e.GetWorkflowExecutionSignaledEventAttributes() != nil:
		at := e.GetWorkflowExecutionSignaledEventAttributes()
		out.Summary, out.Payload = at.GetSignalName(), payloadsJSON(at.GetInput())
	case e.GetActivityTaskScheduledEventAttributes() != nil:
		at := e.GetActivityTaskScheduledEventAttributes()
		out.Summary = at.GetActivityType().GetName() + " on " + at.GetTaskQueue().GetName()
		out.Payload = payloadsJSON(at.GetInput())
	case e.GetActivityTaskStartedEventAttributes() != nil:
		at := e.GetActivityTaskStartedEventAttributes()
		out.Summary = fmt.Sprintf("attempt %d by %s", at.GetAttempt(), at.GetIdentity())
	case e.GetActivityTaskCompletedEventAttributes() != nil:
		out.Payload = payloadsJSON(e.GetActivityTaskCompletedEventAttributes().GetResult())
	case e.GetActivityTaskFailedEventAttributes() != nil:
		out.Failure, _ = failureOf(e.GetActivityTaskFailedEventAttributes().GetFailure())
	case e.GetActivityTaskTimedOutEventAttributes() != nil:
		out.Failure, _ = failureOf(e.GetActivityTaskTimedOutEventAttributes().GetFailure())
	case e.GetTimerStartedEventAttributes() != nil:
		at := e.GetTimerStartedEventAttributes()
		out.Summary = "timer " + at.GetTimerId() + " " + at.GetStartToFireTimeout().AsDuration().String()
	case e.GetStartChildWorkflowExecutionInitiatedEventAttributes() != nil:
		at := e.GetStartChildWorkflowExecutionInitiatedEventAttributes()
		out.Summary = at.GetWorkflowType().GetName() + " " + at.GetWorkflowId() + " on " + at.GetTaskQueue().GetName()
		out.Payload = payloadsJSON(at.GetInput())
	case e.GetChildWorkflowExecutionCompletedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionCompletedEventAttributes()
		out.Summary, out.Payload = at.GetWorkflowExecution().GetWorkflowId(), payloadsJSON(at.GetResult())
	case e.GetChildWorkflowExecutionFailedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionFailedEventAttributes()
		out.Summary = at.GetWorkflowExecution().GetWorkflowId()
		out.Failure, _ = failureOf(at.GetFailure())
	case e.GetNexusOperationScheduledEventAttributes() != nil:
		at := e.GetNexusOperationScheduledEventAttributes()
		out.Summary = at.GetEndpoint() + " " + at.GetService() + "/" + at.GetOperation()
		out.Payload = payloadJSON(at.GetInput())
	case e.GetNexusOperationCompletedEventAttributes() != nil:
		out.Payload = payloadJSON(e.GetNexusOperationCompletedEventAttributes().GetResult())
	case e.GetNexusOperationFailedEventAttributes() != nil:
		out.Failure, _ = failureOf(e.GetNexusOperationFailedEventAttributes().GetFailure())
	case e.GetMarkerRecordedEventAttributes() != nil:
		out.Summary = e.GetMarkerRecordedEventAttributes().GetMarkerName()
	case e.GetWorkflowTaskFailedEventAttributes() != nil:
		out.Failure, _ = failureOf(e.GetWorkflowTaskFailedEventAttributes().GetFailure())
	}

	if out.GetPayload() == "null" && out.GetSummary() == "" {
		out.Payload = ""
	}

	return out
}

// CancelRun implements WorkflowService.
func (a WorkflowAPI) CancelRun(
	ctx context.Context, req *consolev1.CancelRunRequest,
) (*consolev1.CancelRunResponse, error) {
	if err := a.act(ctx, "workflow.cancel", req.GetWorkflowId(), req.GetRunId(), func(c client.Client) error {
		return c.CancelWorkflow(ctx, req.GetWorkflowId(), req.GetRunId())
	}); err != nil {
		return nil, err
	}

	return &consolev1.CancelRunResponse{}, nil
}

// TerminateRun implements WorkflowService.
func (a WorkflowAPI) TerminateRun(
	ctx context.Context, req *consolev1.TerminateRunRequest,
) (*consolev1.TerminateRunResponse, error) {
	reason := req.GetReason()
	if reason == "" {
		reason = "terminated from the console"
	}

	reason += " (" + a.o.author(ctx) + ")"

	if err := a.act(ctx, "workflow.terminate", req.GetWorkflowId(), req.GetRunId(), func(c client.Client) error {
		return c.TerminateWorkflow(ctx, req.GetWorkflowId(), req.GetRunId(), reason)
	}, xlog.String("reason", reason)); err != nil {
		return nil, err
	}

	return &consolev1.TerminateRunResponse{}, nil
}

// SignalRun implements WorkflowService.
func (a WorkflowAPI) SignalRun(
	ctx context.Context, req *consolev1.SignalRunRequest,
) (*consolev1.SignalRunResponse, error) {
	if req.GetSignal() == "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w: signal is required", ErrInput))
	}

	var arg any

	if in := []byte(req.GetInput()); len(in) > 0 {
		if !json.Valid(in) {
			return nil, a.o.status(ctx, fmt.Errorf("%w: input is not valid JSON", ErrInput))
		}

		arg = json.RawMessage(in)
	}

	if err := a.act(ctx, "workflow.signal", req.GetWorkflowId(), req.GetRunId(), func(c client.Client) error {
		return c.SignalWorkflow(ctx, req.GetWorkflowId(), req.GetRunId(), req.GetSignal(), arg)
	}, xlog.String("signal", req.GetSignal())); err != nil {
		return nil, err
	}

	return &consolev1.SignalRunResponse{}, nil
}

// act runs one action on a run and audits it.
func (a WorkflowAPI) act(
	ctx context.Context, action, workflowID, runID string, fn func(c client.Client) error, fields ...xlog.Field,
) error {
	if workflowID == "" {
		return a.o.status(ctx, fmt.Errorf("%w: workflow_id is required", ErrInput))
	}

	c, err := a.o.client()
	if err != nil {
		return a.o.status(ctx, err)
	}

	if err := fn(c); err != nil {
		return a.o.status(ctx, err)
	}

	a.o.audit(ctx, action, workflowID, append(fields, xlog.String("run_id", runID))...)

	return nil
}
