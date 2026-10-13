package auth

import (
	"testing"

	"connectrpc.com/connect/v2"
)

// TestInterceptorBlocksThenAllows exercises the interceptor's auth decision: the
// gate rejects until a token is claimed, then accepts that bearer token. (The
// v2 server interceptor reads the Authorization header off the server CallInfo,
// which the transport populates; here we drive the decision directly.)
func TestInterceptorBlocksThenAllows(t *testing.T) {
	g, _ := New(Options{})
	i := &interceptor{gate: g}

	if err := i.check(""); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unclaimed: err = %v, want Unauth", err)
	}
	if err := i.check("Bearer nope"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad token: err = %v, want Unauth", err)
	}

	tok, _ := g.Claim(g.Code(), "o")
	if err := i.check("Bearer " + tok); err != nil {
		t.Fatalf("authorized call failed: %v", err)
	}
}

// TestNewInterceptorIsServerInterceptor is a compile-time-ish guard that the
// constructor returns a v2 ServerInterceptor.
func TestNewInterceptorIsServerInterceptor(t *testing.T) {
	g, _ := New(Options{})
	var _ connect.ServerInterceptor = NewInterceptor(g)
}
