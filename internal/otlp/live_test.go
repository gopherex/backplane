package otlp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestStoredSignals uses only explicitly configured local acceptance endpoints.
// It does not need a module declaration, operator login or service registration.
func TestStoredSignals(t *testing.T) {
	t.Parallel()

	collector := os.Getenv("BACKPLANE_TEST_OTLP")
	if collector == "" {
		t.Skip("set BACKPLANE_TEST_OTLP and the three BACKPLANE_TEST_*_URL storage endpoints")
	}

	logs, traces, metrics := os.Getenv("BACKPLANE_TEST_LOGS_URL"), os.Getenv("BACKPLANE_TEST_TRACES_URL"), os.Getenv("BACKPLANE_TEST_METRICS_URL")
	if logs == "" || traces == "" || metrics == "" {
		t.Fatal("all three explicit storage endpoints are required")
	}

	handler := admission(t, settings(collector))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &http.Client{Timeout: 10 * time.Second}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	nanos := time.Now().Add(-time.Minute).UnixNano()
	resource := func(service string) string {
		return fmt.Sprintf(`{"attributes":[{"key":"service.namespace","value":{"stringValue":"backplane-acceptance"}},{"key":"service.name","value":{"stringValue":%q}},{"key":"acceptance.id","value":{"stringValue":%q}}]}`, service, id)
	}

	payloads := map[string]string{
		"logs":    fmt.Sprintf(`{"resourceLogs":[{"resource":%s,"scopeLogs":[{"scope":{"name":"fixture"},"logRecords":[{"timeUnixNano":"%d","traceId":%q,"spanId":"0000000000000001","body":{"kvlistValue":{"values":[{"key":"count","value":{"intValue":"9007199254740993"}},{"key":"nested","value":{"kvlistValue":{"values":[{"key":"message","value":{"stringValue":"structured body"}}]}}}]}}}]}]}]}`, resource("undeclared-external"), nanos, id),
		"traces":  fmt.Sprintf(`{"resourceSpans":[{"resource":%s,"scopeSpans":[{"scope":{"name":"fixture"},"spans":[{"traceId":%q,"spanId":"0000000000000001","name":"wrapper","kind":2,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]},{"resource":%s,"scopeSpans":[{"scope":{"name":"fixture"},"spans":[{"traceId":%q,"spanId":"0000000000000002","parentSpanId":"0000000000000001","name":"kratos","kind":2,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`, resource("wrapper"), id, nanos, nanos+123456789, resource("kratos"), id, nanos+1, nanos+123456788),
		"metrics": fmt.Sprintf(`{"resourceMetrics":[{"resource":%s,"scopeMetrics":[{"scope":{"name":"fixture"},"metrics":[{"name":"backplane_acceptance","gauge":{"dataPoints":[{"timeUnixNano":"%d","asDouble":42,"attributes":[{"key":"acceptance_id","value":{"stringValue":%q}}]}]}}]}]}]}`, resource("undeclared-external"), nanos, id),
	}
	for signal, payload := range payloads {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/"+signal, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		body, _ := io.ReadAll(response.Body)
		response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s admission: %s %s", signal, response.Status, body)
		}
	}

	server.Close()

	directID := strings.ReplaceAll(uuid.NewString(), "-", "")
	directPayload := strings.ReplaceAll(payloads["logs"], id, directID)
	directRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, collector+"/v1/logs", strings.NewReader(directPayload))
	directRequest.Header.Set("Content-Type", "application/json")

	directResponse, err := client.Do(directRequest)
	if err != nil {
		t.Fatal(err)
	}

	directResponse.Body.Close()

	if directResponse.StatusCode != http.StatusOK {
		t.Fatalf("Collector requires Backplane: %s", directResponse.Status)
	}

	// Flush only this explicit test stack. In v0.12 trace flush requires POST.
	for _, endpoint := range []string{logs, traces, metrics} {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/internal/force_flush", http.NoBody)

		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Fatalf("flush %s: %s", endpoint, response.Status)
		}
	}

	query := func(endpoint string) []byte {
		t.Helper()
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, http.NoBody)

		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("query: %s %s", response.Status, body)
		}

		return body
	}

	logBody := query(logs + "/select/logsql/query?query=" + url.QueryEscape(`acceptance.id:"`+id+`"`) + "&limit=10")
	for _, want := range []string{"9007199254740993", "structured body", "undeclared-external", id, time.Unix(0, nanos).UTC().Format(time.RFC3339Nano)} {
		if !bytes.Contains(logBody, []byte(want)) {
			t.Fatalf("stored log missing %q: %s", want, logBody)
		}
	}

	if bytes.Contains(logBody, []byte("__backplane_otlp_v1")) {
		t.Fatal("proxy injected a private storage representation")
	}

	directLog := query(logs + "/select/logsql/query?query=" + url.QueryEscape(`acceptance.id:"`+directID+`"`) + "&limit=10")
	if !bytes.Contains(directLog, []byte("structured body")) || !bytes.Contains(directLog, []byte(directID)) {
		t.Fatalf("independent Collector ingestion missing: %s", directLog)
	}

	// The trace-id index flush is independent of raw-span force_flush.
	var traceBody []byte

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		traceBody = query(traces + "/select/tempo/api/v2/traces/" + id)
		if bytes.Contains(traceBody, []byte("wrapper")) && bytes.Contains(traceBody, []byte("kratos")) {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	for _, want := range []string{"wrapper", "kratos", strconv.FormatInt(nanos, 10), strconv.FormatInt(nanos+123456789, 10)} {
		if !bytes.Contains(traceBody, []byte(want)) {
			t.Fatalf("stored trace missing %q: %s", want, traceBody)
		}
	}

	if bytes.Contains(traceBody, []byte("__backplane_otlp_v1")) {
		t.Fatal("proxy injected private span storage metadata")
	}

	metricBody := query(metrics + "/api/v1/query?query=" + url.QueryEscape(`backplane_acceptance{acceptance_id="`+id+`"}`) + "&time=" + strconv.FormatInt(nanos/1e9+2, 10))

	var result struct {
		Data struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(metricBody, &result); err != nil || len(result.Data.Result) != 1 || len(result.Data.Result[0].Value) != 2 || string(result.Data.Result[0].Value[1]) != `"42"` {
		t.Fatalf("stored metric: %s (%v)", metricBody, err)
	}

	checkObs(t, logs, traces, metrics, id, nanos)
}
