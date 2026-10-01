package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

var (
	errExportConfig      = errors.New("audit export requires a clean HTTP(S) URL and single-line authorization")
	errExportRequest     = errors.New("create audit export request")
	errExportUnavailable = errors.New("audit export endpoint unavailable")
	errExportResponse    = errors.New("invalid audit export response")
	errExportRejected    = errors.New("audit export record rejected")
)

func validateExport(s Settings) error {
	if s.ExportURL == "" {
		return nil
	}

	parsed, err := url.Parse(s.ExportURL)
	if err != nil ||
		(parsed.Scheme != "http" &&
			parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		strings.ContainsAny(s.ExportAuthorization.Reveal(),
			"\r\n") {
		return errExportConfig
	}

	return nil
}

// Exporter synchronously exports one committed record. The caller owns retries
// and acknowledges its PostgreSQL outbox only after the Collector accepts it.
// Redirects are forbidden so authorization never reaches a different endpoint.
func Exporter(settings Settings) func(context.Context, Entry) error {
	client := &http.Client{
		Timeout: publishTimeout,
		CheckRedirect: func(_ *http.Request,
			_ []*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}

	return func(ctx context.Context, entry Entry) error {
		record, err := exportRecord(entry)
		if err != nil {
			return err
		}

		payload, err := proto.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode audit export: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.ExportURL, bytes.NewReader(payload))
		if err != nil {
			return errExportRequest
		}

		req.Header.Set("Content-Type", "application/x-protobuf")

		if auth := settings.ExportAuthorization.Reveal(); auth != "" {
			req.Header.Set("Authorization", auth)
		}

		response, err := client.Do(req)
		if err != nil {
			return errExportUnavailable
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: HTTP status %d", errExportRejected, response.StatusCode)
		}

		const maxResponse = 64 << 10

		body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
		if err != nil || len(body) > maxResponse {
			return errExportResponse
		}

		var result collogspb.ExportLogsServiceResponse
		if err = proto.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("decode audit export acknowledgment: %w", err)
		}

		if result.GetPartialSuccess().GetRejectedLogRecords() > 0 {
			return errExportRejected
		}

		return nil
	}
}

func exportRecord(entry Entry) (*collogspb.ExportLogsServiceRequest, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode audit entry: %w", err)
	}

	attr := func(key, value string) *commonpb.KeyValue {
		return &commonpb.KeyValue{
			Key:   key,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}},
		}
	}
	record := &logspb.LogRecord{
		TimeUnixNano:   uint64(entry.CreatedAt.UnixNano()),
		SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO, SeverityText: "INFO", EventName: entry.Action,
		Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: string(body)}},
		Attributes: []*commonpb.KeyValue{
			{Key: LabelAudit, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}},
			attr(LabelID, entry.ID), attr(LabelOrigin, "platform"), attr(LabelActor, entry.Actor),
			attr(LabelSubject, entry.Subject), attr(LabelOutcome, entry.Outcome), attr(attrEvent, entry.Action),
			attr("backplane.audit.service", entry.Service), attr("backplane.audit.operation_id", entry.OperationID),
		},
	}

	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{attr(attrService, "backplane")}},
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope:      &commonpb.InstrumentationScope{Name: "backplane.audit"},
			LogRecords: []*logspb.LogRecord{record},
		}},
	}}}, nil
}
