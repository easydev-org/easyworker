package auth

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
)

// interceptor enforces bearer auth on the WorkerService handlers, while
// leaving the WorkerEnroll handlers untouched. It is a ServerInterceptor passed
// to connect.NewServer.
//
// Fail-closed: every WorkerService call is rejected with Unauthenticated unless
// the request carries an accepted token. The enroll service is mounted on its
// own server (see cmd/easyworker) and manages its own gate.
type interceptor struct {
	gate *Gate
}

func NewInterceptor(g *Gate) connect.ServerInterceptor {
	i := &interceptor{gate: g}
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			authz := ""
			if info, ok := connect.CallInfoForServerContext(ctx); ok {
				authz = info.RequestHeader().Get("Authorization")
			}
			if err := i.check(authz); err != nil {
				return err
			}
			return next(ctx, spec, stream)
		}
	}
}

func (i *interceptor) check(authorization string) error {
	if i.gate.Disabled() {
		return nil
	}
	tok := bearer(authorization)
	if i.gate.Authorized(tok) {
		return nil
	}
	return connect.NewError(connect.CodeUnauthenticated, errUnauthenticated().Error())
}

func bearer(header string) string {
	const p = "Bearer "
	if strings.HasPrefix(header, p) {
		return strings.TrimSpace(strings.TrimPrefix(header, p))
	}
	return ""
}

func errUnauthenticated() error {
	return errString("worker: unauthenticated (missing or invalid bearer token)")
}

type errString string

func (e errString) Error() string { return string(e) }
