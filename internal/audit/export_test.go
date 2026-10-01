package audit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

func TestExportAcknowledgment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
		body   []byte
		fail   bool
	}{
		{"accepted", 200, nil, false},
		{"unavailable", 503, nil, true},
		{"malformed", 200, []byte("broken"), true},
		{"redirect", 307, nil, true},
		{"oversized", 200, make([]byte, 65537), true},
		{"partial", 200, mustExportResponse(t, &collogspb.ExportLogsServiceResponse{PartialSuccess: &collogspb.ExportLogsPartialSuccess{RejectedLogRecords: 1}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("Authorization") != "Bearer private" {
					t.Error("missing export headers")
				}

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}

				var request collogspb.ExportLogsServiceRequest
				if err := proto.Unmarshal(body, &request); err != nil {
					t.Error(err)
				}

				record := request.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
				if stringAttr(record.GetAttributes(), LabelID) != "stable-id" {
					t.Error("lost deduplication id")
				}

				rows, rejected := Records(&request, time.Now())
				if len(rows) != 0 || rejected != 0 {
					t.Fatal("export loops back into application audit")
				}

				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()

			err := Exporter(Settings{ExportURL: server.URL, ExportAuthorization: "Bearer private"})(context.Background(), Entry{ID: "stable-id", CreatedAt: time.Now(), Action: "schedule.create"})
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v want failure=%v", err, tc.fail)
			}
		})
	}
}

func mustExportResponse(t *testing.T, response *collogspb.ExportLogsServiceResponse) []byte {
	t.Helper()

	body, err := proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestExportConfiguration(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{"ftp://example.com", "http://user:pass@example.com", "http://example.com?secret=x", "http://example.com/#token", "http:///missing"} {
		if (Settings{ExportURL: endpoint}).Validate() == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}

	if (Settings{ExportURL: "http://collector:4318/v1/logs"}).Validate() != nil {
		t.Fatal("valid endpoint rejected")
	}
}
