package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/internal/store/db"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// Errors of Sessions.
var (
	// ErrNoToken: no admin token is stored yet.
	ErrNoToken = errors.New("console: no admin token")
	// ErrNoSession: no such session.
	ErrNoSession = errors.New("console: no such session")
)

// Session is one login.
type Session struct {
	ID         uuid.UUID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	Address    string
	UserAgent  string
}

// IsZero reports whether s is the zero Session (no session).
func (s Session) IsZero() bool { return s.ID == uuid.Nil }

// Sessions is where the admin token hash and the sessions live: PostgreSQL
// (PG) in the server, memory in tests.
type Sessions interface {
	// AdminToken is the stored hash; ErrNoToken when there is none.
	AdminToken(ctx context.Context) (string, error)
	// InitAdminToken stores hash unless a token is stored already; false
	// when one was (another replica won the first start).
	InitAdminToken(ctx context.Context, hash string) (bool, error)
	// Rotate replaces the token hash and deletes every session but keep,
	// atomically; it reports how many were deleted.
	Rotate(ctx context.Context, hash string, keep uuid.UUID) (int64, error)

	// Create stores a session under the hash of its token; s.ID is ignored
	// and the new id returned.
	Create(ctx context.Context, tokenHash []byte, s Session) (uuid.UUID, error)
	// ByToken finds a session by the hash of its token; ErrNoSession.
	ByToken(ctx context.Context, tokenHash []byte) (Session, error)
	// Get finds a session by id; ErrNoSession.
	Get(ctx context.Context, id uuid.UUID) (Session, error)
	// Touch moves last seen forward to at (never back).
	Touch(ctx context.Context, id uuid.UUID, at time.Time) error
	// List is every session, newest first.
	List(ctx context.Context) ([]Session, error)
	// Delete removes a session; false when there was none.
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
	// DeleteStale removes sessions expired at now or idle since idleSince.
	DeleteStale(ctx context.Context, now, idleSince time.Time) (int64, error)
}

// PG keeps sessions in PostgreSQL (schema backplane): console_admin and
// console_session.
type PG struct {
	st deps.Dependency[*store.Store]
}

var _ Sessions = PG{}

// NewPG is the PostgreSQL Sessions over the store; the store is read once
// it is provided (from the console's start on).
func NewPG(st deps.Dependency[*store.Store]) PG { return PG{st: st} }

func (p PG) q() *db.Queries { return p.st.Get().Q }

// AdminToken implements Sessions.
func (p PG) AdminToken(ctx context.Context) (string, error) {
	row, err := p.q().GetAdminToken(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoToken
	}

	if err != nil {
		return "", fmt.Errorf("console: admin token: %w", err)
	}

	return row.TokenHash, nil
}

// InitAdminToken implements Sessions.
func (p PG) InitAdminToken(ctx context.Context, hash string) (bool, error) {
	n, err := p.q().InitAdminToken(ctx, hash)
	if err != nil {
		return false, fmt.Errorf("console: admin token: %w", err)
	}

	return n == 1, nil
}

// Rotate implements Sessions.
func (p PG) Rotate(ctx context.Context, hash string, keep uuid.UUID) (int64, error) {
	var revoked int64

	st := p.st.Get()

	err := st.InTx(ctx, func(ctx context.Context) error {
		if err := st.Q.SetAdminToken(ctx, hash); err != nil {
			return err //nolint:wrapcheck // wrapped below
		}

		n, err := st.Q.DeleteOtherSessions(ctx, keep)
		revoked = n

		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return 0, fmt.Errorf("console: rotate: %w", err)
	}

	return revoked, nil
}

// Create implements Sessions.
func (p PG) Create(ctx context.Context, tokenHash []byte, s Session) (uuid.UUID, error) {
	row, err := p.q().CreateSession(ctx, db.CreateSessionParams{
		TokenHash: tokenHash, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, Address: s.Address, UserAgent: s.UserAgent,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("console: create session: %w", err)
	}

	return row.ID, nil
}

// ByToken implements Sessions.
func (p PG) ByToken(ctx context.Context, tokenHash []byte) (Session, error) {
	r, err := p.q().GetSessionByToken(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}

	if err != nil {
		return Session{}, fmt.Errorf("console: session: %w", err)
	}

	return Session(r), nil
}

// Get implements Sessions.
func (p PG) Get(ctx context.Context, id uuid.UUID) (Session, error) {
	r, err := p.q().GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}

	if err != nil {
		return Session{}, fmt.Errorf("console: session: %w", err)
	}

	return Session(r), nil
}

// Touch implements Sessions.
func (p PG) Touch(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := p.q().TouchSession(ctx, db.TouchSessionParams{ID: id, LastSeenAt: at}); err != nil {
		return fmt.Errorf("console: touch session: %w", err)
	}

	return nil
}

// List implements Sessions.
func (p PG) List(ctx context.Context) ([]Session, error) {
	rows, err := p.q().ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("console: sessions: %w", err)
	}

	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, Session(r))
	}

	return out, nil
}

// Delete implements Sessions.
func (p PG) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := p.q().DeleteSession(ctx, id)
	if err != nil {
		return false, fmt.Errorf("console: delete session: %w", err)
	}

	return n > 0, nil
}

// DeleteStale implements Sessions.
func (p PG) DeleteStale(ctx context.Context, now, idleSince time.Time) (int64, error) {
	n, err := p.q().DeleteStaleSessions(ctx, db.DeleteStaleSessionsParams{Now: now, IdleSince: idleSince})
	if err != nil {
		return 0, fmt.Errorf("console: delete stale sessions: %w", err)
	}

	return n, nil
}
