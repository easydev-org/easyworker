package internal

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	"github.com/easydev-org/easyworker/internal/auth"
)

// EnrollService implements workerv1connect.WorkerEnrollHandler: the one-time,
// exclusive claim face. It is only meaningful while the gate is Unclaimed —
// claim succeeds exactly once, returns the worker-issued bearer token, and
// permanently closes the face (later claims → AlreadyExists).
type EnrollService struct {
	gate *auth.Gate
}

func NewEnrollService(gate *auth.Gate) *EnrollService { return &EnrollService{gate: gate} }

func (s *EnrollService) Status(ctx context.Context, req *workerv1.EnrollStatusRequest) (*workerv1.EnrollStatusResponse, error) {
	st := s.gate.Status()
	return &workerv1.EnrollStatusResponse{
		Claimed:       st.Claimed,
		NeedsCode:     st.NeedsCode,
		Preauthorized: st.Preauthorized,
		BootId:        st.BootID,
	}, nil
}

func (s *EnrollService) Claim(ctx context.Context, req *workerv1.EnrollClaimRequest) (*workerv1.EnrollClaimResponse, error) {
	tok, err := s.gate.Claim(req.Code, req.OwnerId)
	switch {
	case err == nil:
		return &workerv1.EnrollClaimResponse{Ok: true, Token: tok}, nil
	case errors.Is(err, auth.ErrAlreadyClaimed):
		return nil, connect.NewError(connect.CodeAlreadyExists, err.Error()).WithCause(err)
	case errors.Is(err, auth.ErrRateLimited):
		return nil, connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
	default:
		return nil, connect.NewError(connect.CodeUnauthenticated, err.Error()).WithCause(err)
	}
}

// Unrelease revokes the caller's token (verified from the Authorization
// header) and returns a fresh one-time code, putting the worker back into the
// claimable Unclaimed state for any caller.
func (s *EnrollService) Unrelease(ctx context.Context, req *workerv1.EnrollUnreleaseRequest) (*workerv1.EnrollUnreleaseResponse, error) {
	info, _ := connect.CallInfoForServerContext(ctx)
	bearer := bearerOf(info.RequestHeader().Get("Authorization"))
	code, err := s.gate.Release(bearer, req.OwnerId)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err.Error()).WithCause(err)
	}
	return &workerv1.EnrollUnreleaseResponse{Ok: true, Code: code}, nil
}

func bearerOf(header string) string {
	const p = "Bearer "
	if len(header) > len(p) && header[:len(p)] == p {
		return header[len(p):]
	}
	return ""
}
