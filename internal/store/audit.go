package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/gopherex/backplane/internal/store/db"
)

// AuditDetail permits control metadata, never arbitrary request bodies, secrets,
// configuration values, workflow arguments, comments or backend error messages.
type AuditDetail struct {
	Revision   int64    `json:"revision,omitempty"`
	RollbackOf int64    `json:"rollback_of,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Paused     *bool    `json:"paused,omitempty"`
	Code       string   `json:"code,omitempty"`
	WorkflowID string   `json:"workflow_id,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	Affected   int64    `json:"affected,omitempty"`
}

// AuditDraft is one semantic action or one phase of an external command. Actor
// uses the established console:<session UUID> identity, or a named system actor.
type AuditDraft struct {
	Actor       string
	Action      string
	Subject     string
	Outcome     string
	OperationID uuid.UUID
	Detail      AuditDetail
}

// AppendAudit joins the caller's control transaction or opens its own. The
// clock's row lock survives until commit, preventing a later visible sequence
// from overtaking this entry. Outbox work is committed with the entry.
func (s *Store) AppendAudit(ctx context.Context, draft AuditDraft) (db.InsertAuditEntryRow, error) {
	id := uuid.New()
	if draft.OperationID == uuid.Nil {
		draft.OperationID = id
	}

	detail, err := json.Marshal(draft.Detail)
	if err != nil {
		return db.InsertAuditEntryRow{}, fmt.Errorf("audit detail: %w", err)
	}

	var entry db.InsertAuditEntryRow

	err = s.InTx(ctx, func(ctx context.Context) error {
		sequence, seqErr := s.Q.NextAuditSequence(ctx)
		if seqErr != nil {
			return fmt.Errorf("audit sequence: %w", seqErr)
		}

		var insertErr error

		entry, insertErr = s.Q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
			Sequence: sequence.Sequence, ID: id, Actor: draft.Actor, Action: draft.Action, Subject: draft.Subject,
			Outcome: draft.Outcome, OperationID: draft.OperationID, Detail: detail,
		})
		if insertErr != nil {
			return fmt.Errorf("audit entry: %w", insertErr)
		}

		if outboxErr := s.Q.InsertAuditOutbox(ctx, sequence.Sequence); outboxErr != nil {
			return fmt.Errorf("audit outbox: %w", outboxErr)
		}

		return nil
	})

	return entry, err
}

// AuditControl is the transaction participant for a completed database mutation.
func (s *Store) AuditControl(ctx context.Context, actor, action, subject string, detail AuditDetail) error {
	_, err := s.AppendAudit(ctx, AuditDraft{
		Actor:   actor,
		Action:  action,
		Subject: subject,
		Outcome: "succeeded",
		Detail:  detail,
	})

	return err
}
