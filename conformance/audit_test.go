package conformance_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/audit"
)

func (w *replicaWorld) auditDelivered(t *testing.T) {
	t.Helper()

	var history consolev1.SearchAuditResponse
	w.call(t, "/backplane.console.v1.AuditService/SearchAudit", &consolev1.SearchAuditRequest{PageSize: 500, Filter: platformOnly()}, &history)

	wanted := make(map[string]*consolev1.AuditRecord)
	actions := make(map[string]bool)

	for _, entry := range history.GetRecords() {
		wanted[entry.GetId()] = entry
		actions[entry.GetAction()] = true
	}

	for _, action := range []string{"session.create", "binding.save", "rule.save", "config.save", "hook.call"} {
		if !actions[action] {
			t.Fatalf("missing durable audit for %s", action)
		}
	}

	if history.GetNextPageCursor() != "" {
		t.Fatal("acceptance fixture unexpectedly exceeds one audit page")
	}

	m1Until(t, m2Wait, "durable audit delivered through real SDK and NATS", func() (bool, string) {
		var messages consolev1.PeekMessagesResponse

		err := m1Call(t.Context(), w.cc, "/backplane.console.v1.EventService/PeekMessages",
			&consolev1.PeekMessagesRequest{Service: "backplane", Event: "AuditEntry", Limit: 500}, &messages)
		if err != nil {
			return false, err.Error()
		}

		for _, message := range messages.GetMessages() {
			entry, exists := wanted[message.GetCloudEvent().GetId()]
			if !exists {
				continue
			}

			var event audit.Entry
			if err := json.Unmarshal([]byte(message.GetData()), &event); err != nil {
				t.Fatal(err)
			}

			if event.ID != entry.GetId() || event.Sequence != strconv.FormatUint(entry.GetSequence(), 10) ||
				event.Action != entry.GetAction() || event.OperationID != entry.GetOperationId() ||
				event.Outcome != entry.GetOutcome() {
				t.Fatalf("event differs from committed audit: %+v / %v", event, entry)
			}

			delete(wanted, event.ID)
		}

		return len(wanted) == 0, fmt.Sprintf("%d audit events pending", len(wanted))
	})
}

// platformOnly is the audit feed's platform entries.
func platformOnly() *consolev1.AuditFilter {
	return &consolev1.AuditFilter{Conditions: []*consolev1.AuditCondition{{
		Target: &consolev1.AuditCondition_Field{Field: consolev1.AuditField_AUDIT_FIELD_SOURCE},
		Op:     consolev1.AuditOperator_AUDIT_OPERATOR_IS, Values: []*structpb.Value{structpb.NewStringValue("platform")},
	}}}
}
