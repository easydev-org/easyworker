// Package client is the reusable agent-worker client: it performs the one-time
// exclusive enrollment handshake and builds a bearer-authenticated
// WorkerService client. Any service (easylab, a controller, an operator CLI)
// can import it without depending on easylab.
//
// Enrollment (external sandbox / host runner path):
//
//	tok, err := client.Enroll(ctx, "http://host:8080", code, "my-service")
//	cli := client.Dial("http://host:8080", tok) // workerv1connect.WorkerServiceClient
//
// Managed sandboxes instead receive WORKER_TOKEN via env and only need:
//
//	cli := client.Dial("http://127.0.0.1:PORT", tok)
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	"github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
)

// Enroll performs the one-time exclusive claim against an unclaimed worker and
// returns the worker-issued bearer token. ownerID is recorded by the worker for
// audit (may be empty).
//
// A worker that is already claimed (or pre-authorized) returns
// ErrAlreadyClaimed — enrollment is exclusive and cannot be transferred.
func Enroll(ctx context.Context, baseURL, code, ownerID string) (string, error) {
	ec := NewEnrollClient(baseURL)
	res, err := ec.Claim(ctx, &workerv1.EnrollClaimRequest{
		Code: code, OwnerId: ownerID,
	})
	if err != nil {
		if connect.CodeOf(err) == connect.CodeAlreadyExists {
			return "", ErrAlreadyClaimed
		}
		return "", err
	}
	if res == nil || res.Token == "" {
		return "", fmt.Errorf("worker returned no token")
	}
	return res.Token, nil
}

// Status queries the worker's enrollment state (useful to detect a restarted,
// unclaimed worker).
func Status(ctx context.Context, baseURL string) (*workerv1.EnrollStatusResponse, error) {
	res, err := NewEnrollClient(baseURL).Status(ctx, &workerv1.EnrollStatusRequest{})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Bearer returns a connect.ClientInterceptor that attaches `Authorization: Bearer
// <token>` to every request — unary AND streaming (v2 interceptors cover both).
func Bearer(token string) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if token != "" {
				var info *connect.CallInfo
				ctx, info = connect.NewClientContext(ctx)
				info.RequestHeader().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, spec)
		}
	}
}

// Dial builds a bearer-authenticated WorkerService client.
func Dial(baseURL, token string, opts ...connect.ClientInterceptor) workerv1connect.WorkerServiceClient {
	hc := &http.Client{}
	interceptors := append([]connect.ClientInterceptor{}, opts...)
	interceptors = append(interceptors, Bearer(token))
	return workerv1connect.NewWorkerServiceClient(connect.NewClient(
		connecthttp.NewTransport(hc, trimSlash(baseURL), connecthttp.WithReadMaxBytes(0)),
		interceptors...,
	))
}

// NewEnrollClient builds a WorkerEnroll client (no auth; the claim face is
// open only while the worker is unclaimed).
func NewEnrollClient(baseURL string) workerv1connect.WorkerEnrollClient {
	return workerv1connect.NewWorkerEnrollClient(connect.NewClient(
		connecthttp.NewTransport(&http.Client{}, trimSlash(baseURL), connecthttp.WithReadMaxBytes(0)),
	))
}

// ErrAlreadyClaimed is returned when the worker already has an owner.
var ErrAlreadyClaimed = errors.New("worker already claimed by another caller")

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Release revokes the worker's current token (must equal bearer) and returns a
// fresh one-time code, putting the worker back into the claimable state. Only
// a claimed-by-code worker can be released (managed/pre-authorized cannot).
func Release(ctx context.Context, baseURL, bearer, ownerID string) (string, error) {
	hc := &http.Client{Timeout: 15 * time.Second}
	var interceptors []connect.ClientInterceptor
	if bearer != "" {
		interceptors = append(interceptors, Bearer(bearer))
	}
	res, err := workerv1connect.NewWorkerEnrollClient(connect.NewClient(
		connecthttp.NewTransport(hc, trimSlash(baseURL), connecthttp.WithReadMaxBytes(0)),
		interceptors...,
	)).Unrelease(ctx, &workerv1.EnrollUnreleaseRequest{OwnerId: ownerID})
	if err != nil {
		return "", err
	}
	return res.GetCode(), nil
}
