package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/store/db"
)

const (
	defaultPage    = 100
	maxPage        = 500
	maxCursorBytes = 2048
	maxFilterBytes = 2048
)

type cursor struct {
	Version      int    `json:"version"`
	Installation string `json:"installation"`
	Filter       string `json:"filter"`
	Position     int64  `json:"position"`
	Snapshot     int64  `json:"snapshot"`
	Kind         string `json:"kind"`
}

func rpcError(code codes.Code, message string) error {
	return status.Error(code, message) //nolint:wrapcheck // RPC boundary
}

func encodeCursor(c cursor) string {
	encoded, _ := json.Marshal(c) //nolint:errchkjson // primitive struct cannot fail
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(raw, kind, installation, filter string) (cursor, error) {
	if len(raw) > maxCursorBytes {
		return cursor{}, rpcError(codes.InvalidArgument, "invalid audit cursor")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor{}, rpcError(codes.InvalidArgument, "invalid audit cursor")
	}

	var c cursor
	if err := json.Unmarshal(decoded, &c); err != nil || c.Version != 1 || c.Kind != kind ||
		c.Installation != installation || c.Filter != filter || c.Position < 0 || c.Snapshot < c.Position {
		return cursor{}, rpcError(codes.InvalidArgument, "audit cursor does not match installation/filter")
	}

	return c, nil
}

func filterParams(filter *consolev1.AuditFilter) (db.ListAuditEntriesParams, string, error) {
	params := db.ListAuditEntriesParams{
		Actor: filter.GetActor(), Action: filter.GetAction(), Subject: filter.GetSubject(),
		Outcome: filter.GetOutcome(), OperationID: filter.GetOperationId(), Service: filter.GetService(),
		StartAt: time.Unix(0, 0), EndAt: time.Unix(0, math.MaxInt64),
	}

	values := []string{params.Actor, params.Action, params.Subject, params.Outcome, params.OperationID, params.Service}
	for _, value := range values {
		if len(value) > maxFilterBytes {
			return params, "", rpcError(codes.InvalidArgument, "audit filter exceeds byte budget")
		}
	}

	if params.OperationID != "" {
		if _, err := uuid.Parse(params.OperationID); err != nil {
			return params, "", rpcError(codes.InvalidArgument, "invalid audit operation id")
		}
	}

	if filter.GetStart() != nil {
		if err := filter.GetStart().CheckValid(); err != nil {
			return params, "", rpcError(codes.InvalidArgument, "invalid audit start time")
		}

		params.StartAt = filter.GetStart().AsTime()
	}

	if filter.GetEnd() != nil {
		if err := filter.GetEnd().CheckValid(); err != nil {
			return params, "", rpcError(codes.InvalidArgument, "invalid audit end time")
		}

		params.EndAt = filter.GetEnd().AsTime()
	}

	if !params.EndAt.After(params.StartAt) {
		return params, "", rpcError(codes.InvalidArgument, "invalid audit time range")
	}

	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(filter)
	if err != nil {
		return params, "", rpcError(codes.InvalidArgument, "invalid audit filter")
	}

	hash := sha256.Sum256(encoded)

	return params, hex.EncodeToString(hash[:]), nil
}
