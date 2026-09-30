package audit //nolint:testpackage // verifies the audit metadata allowlist without external dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
)

func TestCommandResultsAndRedaction(t *testing.T) {
	t.Parallel()

	failed := &consolev1.CallResult{Error: "secret payload", ErrorType: "secret type", WorkflowId: "flow", RunId: "run"}

	cases := []struct {
		name     string
		response any
		err      error
		outcome  string
	}{
		{"hook failure", &consolev1.CallHookResponse{Result: failed}, nil, "failed"},
		{"activity failure", &consolev1.RunActivityResponse{Result: failed}, nil, "failed"},
		{"binding failure", &consolev1.TestBindingResponse{Result: failed}, nil, "failed"},
		{"rule failure", &consolev1.TestRuleResponse{Result: failed}, nil, "failed"},
		{"evaluation failure", &consolev1.TestRuleResponse{Error: "secret expression"}, nil, "failed"},
		{"dry rule no match", &consolev1.TestRuleResponse{}, nil, "succeeded"},
		{"validation", &consolev1.TestRuleResponse{Violations: []*consolev1.BindingViolation{{Message: "secret"}}}, nil, "rejected"},
		{"partial redrive", &consolev1.RedriveDeadLettersResponse{Redriven: 1, Failed: []*consolev1.SeqError{{Error: "secret"}}}, nil, "partial"},
		{"failed purge", &consolev1.PurgeDeadLettersResponse{Failed: []*consolev1.SeqError{{Error: "secret"}}}, nil, "failed"},
		{"timeout uncertain", nil, status.Error(codes.DeadlineExceeded, "secret"), "unknown"},
		{"invalid input", nil, status.Error(codes.InvalidArgument, "secret"), "rejected"},
		{"successful output", &consolev1.CallHookResponse{Result: &consolev1.CallResult{Output: "secret"}}, nil, "succeeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome, detail := commandResult(tc.response, tc.err)
			if outcome != tc.outcome {
				t.Fatalf("got %s, want %s", outcome, tc.outcome)
			}

			encoded, err := json.Marshal(detail)
			if err != nil || strings.Contains(string(encoded), "secret") {
				t.Fatalf("unsafe detail: %s, %v", encoded, err)
			}

			if result, ok := tc.response.(interface{ GetResult() *consolev1.CallResult }); ok && result.GetResult() == failed {
				if detail.WorkflowID != "flow" || detail.RunID != "run" {
					t.Fatal("lost execution identifiers", detail)
				}
			}
		})
	}
}

func TestCommandSubjectExcludesPayload(t *testing.T) {
	t.Parallel()

	if got := commandSubject(&consolev1.TestRuleRequest{Id: "rule", Event: `{"secret":"token"}`}); got != "id=rule" {
		t.Fatal("rule payload copied into audit", got)
	}

	if got := commandSubject(&consolev1.PublishTestEventRequest{Event: "hello.Greeted", Payload: "secret"}); got != "event=hello.Greeted" {
		t.Fatal(got)
	}

	if got := commandSubject(&consolev1.RunActivityRequest{Activity: "formatter.Format", Input: "secret"}); got != "activity=formatter.Format" {
		t.Fatal(got)
	}
}

func TestCommandService(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request any
		service string
	}{
		{"workflow start", &consolev1.StartWorkflowRequest{Service: "hello", Workflow: "GreetMany"}, "hello"},
		{"schedule", &consolev1.PauseScheduleRequest{Service: "hello", Name: "HourlyReport"}, "hello"},
		{"dead letters", &consolev1.RedriveDeadLettersRequest{Subscriber: "formatter", Consumer: "c"}, "formatter"},
		{"hook call", &consolev1.CallHookRequest{Hook: "hello.Greet"}, "hello"},
		{"activity run", &consolev1.RunActivityRequest{Activity: "formatter.Format"}, "formatter"},
		{"test event", &consolev1.PublishTestEventRequest{Event: "hello.Greeted"}, "hello"},
		{"unsaved binding test", &consolev1.TestBindingRequest{Definition: &consolev1.BindingDefinition{Hook: "hello.Greet"}}, "hello"},
		{"unsaved rule test", &consolev1.TestRuleRequest{Definition: &consolev1.RuleDefinition{Event: "hello.Greeted"}}, "hello"},
		{"binding run", &consolev1.CancelBindingRunRequest{WorkflowId: "binding/hello.Greet/request"}, "hello"},
		{"binding test run", &consolev1.CancelBindingRunRequest{WorkflowId: "test/hello.Greet/request"}, "hello"},
		{"plain run", &consolev1.CancelRunRequest{WorkflowId: "orders-42", RunId: "run"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := (&Service{}).commandService(t.Context(), tc.request); got != tc.service {
				t.Fatalf("got %q, want %q", got, tc.service)
			}
		})
	}
}
