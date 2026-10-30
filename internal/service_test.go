package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	workerv1connect "github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyworker/internal/filesvc"
	"github.com/easydev-org/easyworker/internal/jobsvc"
	"github.com/easydev-org/easyworker/internal/script"
)

func newTestServer(t *testing.T) (client workerv1connect.WorkerServiceClient, cleanup func()) {
	t.Helper()
	ws := t.TempDir()
	runner := script.New(ws)
	store, err := jobsvc.OpenStore(filepath.Join(ws, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(jobsvc.NewManager(runner, store, 1000, 200), filesvc.New(ws), runner, nil)

	mux := http.NewServeMux()
	server := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(server, svc)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)

	c := workerv1connect.NewWorkerServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, srv.URL, connecthttp.WithReadMaxBytes(0))))
	return c, func() { srv.Close(); store.Close() }
}

func TestInfo(t *testing.T) {
	c, done := newTestServer(t)
	defer done()

	info, err := c.Info(context.Background(), &workerv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Shell != "easyscript(bash)" {
		t.Errorf("shell = %q", info.Shell)
	}
	if info.Workspace == "" {
		t.Error("workspace empty")
	}
	if len(info.BootId) != 32 {
		t.Errorf("boot_id = %q, want 32-hex", info.BootId)
	}
}

func TestExecuteWatchLifecycle(t *testing.T) {
	c, done := newTestServer(t)
	defer done()

	ctx := context.Background()
	exec, err := c.Execute(ctx, &workerv1.ExecuteRequest{
		Command: "echo first && sleep 0.3 && echo second && echo warn >&2",
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID := exec.JobId
	if jobID == "" {
		t.Fatal("empty job id")
	}

	// watch: replay + live + done
	stream, err := c.WatchJob(ctx, &workerv1.WatchJobRequest{JobId: jobID})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	var doneEv *workerv1.WatchJobResponse_Done
	for {
		msg2, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		msg := msg2
		switch ev := msg.Event.(type) {
		case *workerv1.WatchJobResponse_Output:
			lines = append(lines, ev.Output)
		case *workerv1.WatchJobResponse_Done_:
			doneEv = ev.Done
		}
	}

	if doneEv == nil || doneEv.ExitCode != 0 {
		t.Fatalf("done event = %+v", doneEv)
	}
	// live fanout carries BOTH stdout and stderr in sequence order
	if len(lines) != 3 || lines[0] != "first" || lines[1] != "second" || lines[2] != "warn" {
		t.Errorf("live lines = %v", lines)
	}

	// wait on the finished job returns immediately
	wait, err := c.JobWait(ctx, &workerv1.JobWaitRequest{JobId: jobID, TimeoutMs: 1000})
	if err != nil || wait.State != "done" {
		t.Fatalf("wait = %s err=%v", wait.State, err)
	}

	// history: all + stream filters
	hist, err := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: 0, End: 0})
	if err != nil || hist.TotalLines != 3 {
		t.Fatalf("all history err=%v total=%d lines=%v", err, hist.TotalLines, hist.Lines)
	}
	stdoutOnly, _ := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: 0, End: 0, Stream: "stdout"})
	if stdoutOnly.TotalLines != 2 || stdoutOnly.Lines[0] != "first" {
		t.Errorf("stdout filter = %v", stdoutOnly.Lines)
	}
	stderrOnly, _ := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: 0, End: 0, Stream: "stderr"})
	if stderrOnly.TotalLines != 1 || stderrOnly.Lines[0] != "warn" {
		t.Errorf("stderr filter = %v", stderrOnly.Lines)
	}

	// negative-start pagination
	last, _ := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: jobID, Start: -1, End: 0})
	if last.TotalLines != 3 || len(last.Lines) != 1 || last.Lines[0] != "warn" {
		t.Errorf("tail-1 = %v", last.Lines)
	}
}

// Regression for the untyped-constant clamp: jobWaitMaxMs=60_000 compared
// against time.Duration meant 60µs, so every JobWait fired instantly with
// "running". A long wait on a job finishing at ~1s must block and report
// done, not return early.
func TestJobWaitHonorsLongTimeouts(t *testing.T) {
	c, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	exec, err := c.Execute(ctx, &workerv1.ExecuteRequest{Command: "sleep 1"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	wait, err := c.JobWait(ctx, &workerv1.JobWaitRequest{JobId: exec.JobId, TimeoutMs: 10000})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if wait.State != "done" {
		t.Fatalf("state = %s want done (waited %v)", wait.State, elapsed)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("JobWait returned after %v — timeout was clamped again", elapsed)
	}
}

func TestFilesRoundTrip(t *testing.T) {
	c, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	if _, err := c.FileWrite(ctx, &workerv1.FileWriteRequest{Path: "d/f.bin", Content: []byte{0, 1, 2, 255}}); err != nil {
		t.Fatal(err)
	}
	rd, err := c.FileRead(ctx, &workerv1.FileReadRequest{Path: "d/f.bin"})
	if err != nil || len(rd.Content) != 4 || rd.Content[3] != 255 {
		t.Fatalf("binary round-trip mismatch: %v err=%v", rd.Content, err)
	}
	ls, err := c.FileList(ctx, &workerv1.FileListRequest{Path: "d"})
	if err != nil || !ls.IsDir || len(ls.Files) != 1 || ls.Files[0].Path != "d/f.bin" {
		t.Fatalf("list: %v err=%v", ls, err)
	}
	if ls.Truncated {
		t.Error("a small listing must not be reported truncated")
	}

	// The entry cap is surfaced as `truncated` (a client that diffs a listing
	// against a base tree MUST refuse to act on a truncated one, or it infers
	// phantom deletions). limit=1 over a 2-entry tree hits the cap.
	if _, err := c.FileWrite(ctx, &workerv1.FileWriteRequest{Path: "d/g.bin", Content: []byte{9}}); err != nil {
		t.Fatal(err)
	}
	tr, err := c.FileList(ctx, &workerv1.FileListRequest{Path: "d", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Truncated || len(tr.Files) != 1 {
		t.Fatalf("capped listing must report truncated=true with 1 entry: truncated=%v n=%d", tr.Truncated, len(tr.Files))
	}

	// escape must fail
	if _, err := c.FileRead(ctx, &workerv1.FileReadRequest{Path: "../../etc/passwd"}); err == nil {
		t.Error("traversal must fail")
	}
}

func TestStdinAndKill(t *testing.T) {
	c, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	exec, err := c.Execute(ctx, &workerv1.ExecuteRequest{Command: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.JobStdin(ctx, &workerv1.JobStdinRequest{JobId: exec.JobId, Data: []byte("hello\n"), Close: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hist, err := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: exec.JobId, Start: 0, End: 0})
		if err == nil && hist.Done && len(hist.Lines) == 1 && hist.Lines[0] == "hello" {
			goto killed
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cat did not complete")
killed:
	exec2, err := c.Execute(ctx, &workerv1.ExecuteRequest{Command: "sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.JobKill(ctx, &workerv1.JobKillRequest{JobId: exec2.JobId}); err != nil {
		t.Fatal(err)
	}
	wait, err := c.JobWait(ctx, &workerv1.JobWaitRequest{JobId: exec2.JobId, TimeoutMs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if wait.State != "killed" {
		t.Errorf("state = %s want killed", wait.State)
	}
}
