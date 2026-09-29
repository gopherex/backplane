package console

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
)

// sessionService serves SessionService.
type sessionService struct {
	consolev1.UnimplementedSessionServiceServer

	c *Console
}

// current is the session of the calling connection.
func current(ctx context.Context) (uuid.UUID, error) {
	cn := connOf(ctx)
	if cn == nil {
		return uuid.Nil, status.Errorf(codes.Unauthenticated, "no session")
	}

	return cn.session, nil
}

// ListSessions implements SessionService.
func (s sessionService) ListSessions(
	ctx context.Context, _ *consolev1.ListSessionsRequest,
) (*consolev1.ListSessionsResponse, error) {
	self, err := current(ctx)
	if err != nil {
		return nil, err
	}

	list, err := s.c.sessions.List(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	now := s.c.now()
	res := &consolev1.ListSessionsResponse{}

	for _, x := range list {
		if !alive(x, now) {
			continue
		}

		res.Sessions = append(res.Sessions, &consolev1.Session{
			Id: x.ID.String(), CreatedAt: timestamppb.New(x.CreatedAt), ExpiresAt: timestamppb.New(x.ExpiresAt),
			LastSeenAt: timestamppb.New(x.LastSeenAt), Address: x.Address, UserAgent: x.UserAgent, Current: x.ID == self,
		})
	}

	return res, nil
}

// RevokeSession implements SessionService.
func (s sessionService) RevokeSession(
	ctx context.Context, req *consolev1.RevokeSessionRequest,
) (*consolev1.RevokeSessionResponse, error) {
	if _, err := current(ctx); err != nil {
		return nil, err
	}

	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "session id %q: %v", req.GetId(), err)
	}

	if _, err := s.c.sessions.Get(ctx, id); errors.Is(err, ErrNoSession) {
		return nil, status.Errorf(codes.NotFound, "no session %s", id)
	}

	s.c.revoke(ctx, id)
	s.c.Log().Info("console: session revoked", xlog.String("session", id.String()))

	return &consolev1.RevokeSessionResponse{}, nil
}

// RotateToken implements SessionService.
func (s sessionService) RotateToken(
	ctx context.Context, _ *consolev1.RotateTokenRequest,
) (*consolev1.RotateTokenResponse, error) {
	self, err := current(ctx)
	if err != nil {
		return nil, err
	}

	token, err := newAdminToken()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	hash, err := hashToken(token)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	revoked, err := s.c.sessions.Rotate(ctx, hash, self)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}

	s.c.conns.close(uuid.Nil, self)
	s.c.Log().Warn("console: admin token rotated", xlog.Int64("revoked_sessions", revoked))

	return &consolev1.RotateTokenResponse{Token: token, RevokedSessions: uint32(revoked)}, nil //nolint:gosec // a count
}
