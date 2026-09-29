// Package audit exposes durable control history and delivers its event outbox.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/store/db"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/event"
)

const (
	pollInterval   = 500 * time.Millisecond
	publishTimeout = 3 * time.Second
	leaseSeconds   = 60
	retrySeconds   = 2
	batchSize      = 16
)

// Entry is the backplane.AuditEntry event. Numeric identities are strings to
// preserve their values across consumers; id is the deduplication identity.
type Entry struct {
	ID          string            `json:"id"`
	Sequence    string            `json:"sequence"`
	CreatedAt   time.Time         `json:"created_at"`
	Actor       string            `json:"actor"`
	Action      string            `json:"action"`
	Subject     string            `json:"subject"`
	Outcome     string            `json:"outcome"`
	OperationID string            `json:"operation_id"`
	Detail      store.AuditDetail `json:"detail"`
}

// Service reads committed history. Delivery failures leave the outbox intact.
type Service struct {
	consolev1.UnimplementedAuditServiceServer
	deps.Component
	store     deps.Dependency[*store.Store]
	publish   func(context.Context, Entry) error
	retention time.Duration
}

// Option replaces integration boundaries for acceptance testing.
type Option func(*Service)

// WithPublisher replaces event delivery; persistence and claiming stay real.
func WithPublisher(publish func(context.Context, Entry) error) Option {
	return func(s *Service) { s.publish = publish }
}

// New starts outbox delivery after the store dependency is ready.
func New(parent deps.Scope, st deps.Dependency[*store.Store], options ...Option) *Service {
	s := &Service{Component: deps.NewComponent(parent, "audit"), store: st}
	ref := event.Declare[Entry](s, "AuditEntry", event.Describe("Durable platform control audit; deduplicate by entry id"))

	s.publish = func(ctx context.Context, entry Entry) error { return ref.Publish(ctx, entry, event.ID(entry.ID)) }
	for _, option := range options {
		option(s)
	}

	s.Go(s.run)
	s.Go(s.cleanupLoop)

	return s
}

// Register exposes the audit API on the authenticated console registrar.
func (s *Service) Register(registrar grpc.ServiceRegistrar) {
	consolev1.RegisterAuditServiceServer(registrar, s)
}

func (s *Service) run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if err := s.Deliver(ctx); err != nil && ctx.Err() == nil {
			s.Log().Warn("audit outbox retry", xlog.Err(err))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Deliver claims a bounded batch. Leases protect acknowledgments from stale
// replicas. A crash after publish and before acknowledgment may redeliver.
func (s *Service) Deliver(ctx context.Context) error {
	st := s.store.Get()
	lease := uuid.New()

	claimed, err := st.Q.ClaimAuditOutbox(ctx, db.ClaimAuditOutboxParams{
		Lease:        &lease,
		LeaseSeconds: leaseSeconds,
		BatchSize:    batchSize,
	})
	if err != nil {
		return fmt.Errorf("claim audit outbox: %w", err)
	}

	var failures []error

	for _, claim := range claimed {
		entry, readErr := st.Q.GetAuditEntry(ctx, claim.Sequence)
		if readErr != nil {
			failures = append(failures, fmt.Errorf("read audit outbox entry: %w", readErr))
			continue
		}

		if deliverErr := s.deliverOne(ctx, entry, lease); deliverErr != nil {
			failures = append(failures, deliverErr)
		}
	}

	return errors.Join(failures...)
}

func (s *Service) deliverOne(ctx context.Context, row db.GetAuditEntryRow, lease uuid.UUID) error {
	var detail store.AuditDetail
	if err := json.Unmarshal(row.Detail, &detail); err != nil {
		return fmt.Errorf("decode audit detail: %w", err)
	}

	entry := Entry{
		ID: row.ID.String(), Sequence: strconv.FormatInt(row.Sequence, 10), CreatedAt: row.CreatedAt,
		Actor: row.Actor, Action: row.Action, Subject: row.Subject,
		Outcome: row.Outcome, OperationID: row.OperationID.String(), Detail: detail,
	}

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	publishErr := s.publish(publishCtx, entry)

	cancel()

	if publishErr != nil {
		retry := db.RetryAuditOutboxParams{Sequence: row.Sequence, Lease: &lease, RetrySeconds: retrySeconds}
		if retryErr := s.store.Get().Q.RetryAuditOutbox(ctx, retry); retryErr != nil {
			return fmt.Errorf("schedule audit retry: %w", retryErr)
		}

		return fmt.Errorf("publish audit entry: %w", publishErr)
	}

	ack := db.AcknowledgeAuditOutboxParams{Sequence: row.Sequence, Lease: &lease}
	if err := s.store.Get().Q.AcknowledgeAuditOutbox(ctx, ack); err != nil {
		return fmt.Errorf("acknowledge audit entry: %w", err)
	}

	return nil
}
