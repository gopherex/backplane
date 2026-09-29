package audit

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/store/db"
)

// ListAudit reads the ordering clock and entries in one consistent snapshot.
func (s *Service) ListAudit(
	ctx context.Context, req *consolev1.ListAuditRequest,
) (*consolev1.ListAuditResponse, error) {
	params, hash, err := filterParams(req.GetFilter())
	if err != nil {
		return nil, err
	}

	size := req.GetPageSize()
	if size == 0 {
		size = defaultPage
	}

	if size > maxPage {
		return nil, rpcError(codes.InvalidArgument, "audit page exceeds maximum")
	}

	params.PageSize = int64(size) + 1
	params.Descending = true
	st := s.store.Get()

	var response *consolev1.ListAuditResponse

	err = st.InTx(ctx, func(ctx context.Context) error {
		clock, clockErr := st.Q.GetAuditClock(ctx)
		if clockErr != nil {
			return rpcError(codes.Unavailable, "audit database unavailable")
		}

		position, positionErr := pagePosition(req.GetPageCursor(), st.Installation.String(), hash, clock.Sequence)
		if positionErr != nil {
			return positionErr
		}

		if cursorErr := checkPosition(position, clock); cursorErr != nil {
			return cursorErr
		}

		params.AfterSequence = clock.RetainedAfter
		params.ThroughSequence = position.Position

		rows, readErr := st.Q.ListAuditEntries(ctx, params)
		if readErr != nil {
			return rpcError(codes.Unavailable, "audit database unavailable")
		}

		response = &consolev1.ListAuditResponse{}

		if len(rows) > int(size) {
			rows = rows[:size]
			position.Position = rows[len(rows)-1].Sequence - 1
			response.NextPageCursor = encodeCursor(position)
		}

		entries, convertErr := entriesPB(rows)
		if convertErr != nil {
			return convertErr
		}

		response.Entries = entries
		position.Kind = "watch"
		position.Position = position.Snapshot
		response.WatchCursor = encodeCursor(position)

		return nil
	})

	return response, databaseError(ctx, err)
}

func checkPosition(position cursor, clock db.GetAuditClockRow) error {
	if position.Position < clock.RetainedAfter || position.Snapshot > clock.Sequence {
		return rpcError(codes.OutOfRange, "audit cursor expired; refetch history")
	}

	return nil
}

// WatchAudit reads deltas from storage. No per-subscriber producer queue exists.
func (s *Service) WatchAudit(
	req *consolev1.WatchAuditRequest, stream grpc.ServerStreamingServer[consolev1.WatchAuditResponse],
) error {
	params, hash, err := filterParams(req.GetFilter())
	if err != nil {
		return err
	}

	position, err := decodeCursor(req.GetAfterCursor(), "watch", s.store.Get().Installation.String(), hash)
	if err != nil {
		return err
	}

	params.PageSize = defaultPage

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		entries, next, readErr := s.delta(stream.Context(), params, position)
		if readErr != nil {
			return readErr
		}

		batch := &consolev1.WatchAuditResponse{Entries: entries, Cursor: encodeCursor(next)}
		if sendErr := stream.Send(batch); sendErr != nil {
			return sendErr //nolint:wrapcheck // stream status
		}

		position = next

		if len(entries) == defaultPage {
			continue
		}

		select {
		case <-stream.Context().Done():
			return databaseError(stream.Context(), stream.Context().Err())
		case <-ticker.C:
		}
	}
}

func (s *Service) delta(
	ctx context.Context, params db.ListAuditEntriesParams, position cursor,
) ([]*consolev1.AuditEntry, cursor, error) {
	var entries []*consolev1.AuditEntry

	st := s.store.Get()
	next := position
	err := st.InTx(ctx, func(ctx context.Context) error {
		clock, clockErr := st.Q.GetAuditClock(ctx)
		if clockErr != nil {
			return rpcError(codes.Unavailable, "audit database unavailable")
		}

		if cursorErr := checkPosition(position, clock); cursorErr != nil {
			return cursorErr
		}

		params.AfterSequence = position.Position
		params.ThroughSequence = clock.Sequence

		rows, readErr := st.Q.ListAuditEntries(ctx, params)
		if readErr != nil {
			return rpcError(codes.Unavailable, "audit database unavailable")
		}

		next.Snapshot = clock.Sequence

		next.Position = clock.Sequence
		if len(rows) == defaultPage {
			next.Position = rows[len(rows)-1].Sequence
		}

		converted, convertErr := entriesPB(rows)
		if convertErr != nil {
			return convertErr
		}

		entries = converted

		return nil
	})

	return entries, next, databaseError(ctx, err)
}

func entriesPB(rows []db.ListAuditEntriesRow) ([]*consolev1.AuditEntry, error) {
	entries := make([]*consolev1.AuditEntry, 0, len(rows))
	for i := range rows {
		row := &rows[i]

		var detail store.AuditDetail
		if err := json.Unmarshal(row.Detail, &detail); err != nil {
			return nil, rpcError(codes.Internal, "invalid stored audit metadata")
		}

		entries = append(entries, &consolev1.AuditEntry{
			Id: row.ID.String(), Sequence: positive(row.Sequence), CreatedAt: timestamppb.New(row.CreatedAt),
			Actor: row.Actor, Action: row.Action, Subject: row.Subject,
			Outcome: row.Outcome, OperationId: row.OperationID.String(),
			Detail: &consolev1.AuditDetail{
				Revision: positive(detail.Revision), RollbackOf: positive(detail.RollbackOf),
				Keys: detail.Keys, Paused: detail.Paused,
				Code: detail.Code, WorkflowId: detail.WorkflowID, RunId: detail.RunID, Affected: positive(detail.Affected),
			},
		})
	}

	return entries, nil
}

func positive(value int64) uint64 { return uint64(max(value, 0)) }

func databaseError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	if ctx.Err() != nil {
		return rpcError(status.Code(status.FromContextError(ctx.Err()).Err()), ctx.Err().Error())
	}

	if _, ok := status.FromError(err); ok {
		return err
	}

	return rpcError(codes.Unavailable, "audit database unavailable")
}

func pagePosition(raw, installation, hash string, sequence int64) (cursor, error) {
	if raw != "" {
		return decodeCursor(raw, "page", installation, hash)
	}

	return cursor{
		Version: 1, Installation: installation, Filter: hash, Position: sequence, Snapshot: sequence, Kind: "page",
	}, nil
}
