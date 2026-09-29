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
