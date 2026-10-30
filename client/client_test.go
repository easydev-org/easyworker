package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	"github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyworker/internal"
	"github.com/easydev-org/easyworker/internal/auth"
	"github.com/easydev-org/easyworker/internal/filesvc"
	"github.com/easydev-org/easyworker/internal/jobsvc"
	"github.com/easydev-org/easyworker/internal/script"
)

// newServer builds a real worker handler (auth gate + enroll + WorkerService)
// on an httptest server, returning it plus the gate that owns the code.
func newServer(t *testing.T) (*httptest.Server, *auth.Gate) {
	t.Helper()
	ws := t.TempDir()
	runner := script.New(ws)
	store, err := jobsvc.OpenStore(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	jobs := jobsvc.NewManager(runner, store, 100, 100)
	files := filesvc.New(ws)
	svc := internal.NewService(jobs, files, runner, nil)
	gate, err := auth.New(auth.Options{BootID: "boot-1"})
	if err != nil {
		t.Fatal(err)
	}
	enroll := internal.NewEnrollService(gate)

	mux := http.NewServeMux()
	server := connect.NewServer(auth.NewInterceptor(gate))
	workerv1connect.RegisterWorkerServiceHandler(server, svc)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv2 := connect.NewServer()
	workerv1connect.RegisterWorkerEnrollHandler(srv2, enroll)
	connecthttp.Mount(mux, srv2, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, gate
}

func TestEnrollThenDial(t *testing.T) {
	srv, gate := newServer(t)
	ctx := context.Background()

	// Status is open while unclaimed.
	st, err := Status(ctx, srv.URL)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.NeedsCode || st.Claimed {
		t.Fatalf("status = %+v", st)
	}

	// Unauthenticated WorkerService use is refused.
	if _, err := Dial(srv.URL, "").Info(ctx, &workerv1.InfoRequest{}); err == nil {
		t.Fatal("unauthenticated Info must fail")
	}

	// Claim, then the issued token works.
	tok, err := Enroll(ctx, srv.URL, gate.Code(), "svc-a")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := Dial(srv.URL, tok).Info(ctx, &workerv1.InfoRequest{}); err != nil {
		t.Fatalf("authed Info failed: %v", err)
	}

	// A second caller cannot take over.
	if _, err := Enroll(ctx, srv.URL, "whatever", "svc-b"); err != ErrAlreadyClaimed {
		t.Fatalf("second enroll err = %v, want ErrAlreadyClaimed", err)
	}
}

// TestStreamingCarriesBearer guards the regression where a unary-only bearer
// interceptor left server-streaming RPCs unauthenticated (WatchJob 401).
func TestStreamingCarriesBearer(t *testing.T) {
	srv, gate := newServer(t)
	ctx := context.Background()
	tok, err := Enroll(ctx, srv.URL, gate.Code(), "svc")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	cli := Dial(srv.URL, tok)

	// Start a short job, then consume the server stream — this exercises
	// WrapStreamingClient; an unauthenticated stream would fail immediately.
	res, err := cli.Execute(ctx, &workerv1.ExecuteRequest{Command: "echo streaming-ok"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	stream, err := cli.WatchJob(ctx, &workerv1.WatchJobRequest{JobId: res.JobId})
	if err != nil {
		t.Fatalf("watch open: %v", err)
	}
	var got string
	var done bool
	for {
		msg, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("watch stream err (auth must be stripped for streams?): %v", err)
		}
		switch ev := msg.Event.(type) {
		case *workerv1.WatchJobResponse_Output:
			got += ev.Output
		case *workerv1.WatchJobResponse_Done_:
			done = true
		}
	}

	if !done {
		t.Fatal("stream did not terminate with Done")
	}
	_ = got
}
