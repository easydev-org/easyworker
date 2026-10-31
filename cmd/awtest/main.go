// Command awtest exercises the easyworker Connect API end-to-end. Built for
// linux/windows/darwin and run ON the target platform against a local (or
// remote) worker: Info, Execute, WatchJob streaming, JobOutput stream
// filters, file round-trip + containment, kill. Platform-appropriate
// commands are chosen at runtime. Exit code 0 = all pass.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/easydev-org/easyworker/client"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	workerv1connect "github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
)

var failures int

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("PASS  %-28s %s\n", name, detail)
	} else {
		failures++
		fmt.Printf("FAIL  %-28s %s\n", name, detail)
	}
}

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "worker base URL")
	token := flag.String("token", "", "bearer token (default $WORKER_TOKEN)")
	code := flag.String("code", "", "one-time enrollment code (claims an unclaimed worker)")
	flag.Parse()

	tok := *token
	if tok == "" {
		tok = os.Getenv("WORKER_TOKEN")
	}
	ctx := context.Background()
	if *code != "" {
		// Claim the worker first (exclusive), then use the issued token.
		t, err := client.Enroll(ctx, *addr, *code, "ewtest")
		if err != nil {
			fmt.Printf("FATAL enroll: %v\n", err)
			os.Exit(1)
		}
		tok = t
	}
	opts := []connect.ClientInterceptor{}
	if tok != "" {
		opts = append(opts, client.Bearer(tok))
	}
	c := workerv1connect.NewWorkerServiceClient(connect.NewClient(connecthttp.NewTransport(&http.Client{Timeout: 65 * time.Second}, *addr, connecthttp.WithReadMaxBytes(0)), opts...))

	// ---- Info ----
	info, err := c.Info(ctx, &workerv1.InfoRequest{})
	if err != nil {
		fmt.Printf("FATAL Info: %v\n", err)
		os.Exit(1)
	}
	// The engine is part of this binary now, so it reports itself as such rather than naming a
	// separate product a reader would go looking for.
	check("info", info.Shell == "builtin",
		fmt.Sprintf("os=%s arch=%s shell=%s boot=%s", info.Os, info.Arch, info.Shell, short(info.BootId)))

	// ---- Execute + watch ----
	exec, err := c.Execute(ctx, &workerv1.ExecuteRequest{
		Command: "echo hello-from-" + info.Os + " && echo to-stderr >&2 && printf tail-no-newline",
	})
	if err != nil {
		fmt.Printf("FATAL Execute: %v\n", err)
		os.Exit(1)
	}
	id := exec.JobId

	var live []string
	var done *workerv1.WatchJobResponse_Done
	st, err := c.WatchJob(ctx, &workerv1.WatchJobRequest{JobId: id})
	if err == nil {
		var streamErr error
		for {
			msg, err := st.Receive()
			if err != nil {
				streamErr = err
				break
			}
			switch ev := msg.Event.(type) {
			case *workerv1.WatchJobResponse_Output:
				live = append(live, ev.Output)
			case *workerv1.WatchJobResponse_Done_:
				done = ev.Done
			}
		}
		if errors.Is(streamErr, io.EOF) {
			streamErr = nil
		}
		err = streamErr
	}
	watchedEcho := contains(live, "hello-from-"+info.Os)
	check("watch-live+done", err == nil && done != nil && watchedEcho,
		fmt.Sprintf("live=%v done.exit=%d", live, doneExit(done)))

	// ---- JobOutput filters (incl. no-trailing-newline flush) ----
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	w, _ := c.JobWait(waitCtx, &workerv1.JobWaitRequest{JobId: id, TimeoutMs: 10000})
	cancel()
	check("jobwait-done", w != nil && w.State == "done", fmt.Sprintf("state=%s exit=%d", stateOf(w), exitOf(w)))

	all := linesOf(c, ctx, id, "all")
	stdout := linesOf(c, ctx, id, "stdout")
	stderr := linesOf(c, ctx, id, "stderr")
	check("output-all", len(all) == 3 && contains(all, "tail-no-newline"), fmt.Sprintf("%v", all))
	check("output-stdout-filter", len(stdout) == 2 && contains(stdout, "hello-from-"+info.Os), fmt.Sprintf("%v", stdout))
	check("output-stderr-filter", len(stderr) == 1 && stderr[0] == "to-stderr", fmt.Sprintf("%v", stderr))

	// ---- external command through builtin interp ----
	extCmd := "uname -s"
	if info.Os == "windows" {
		extCmd = "cmd /c echo external-cmd-works"
	}
	ext := runSimple(c, ctx, extCmd)
	check("external-exec", ext != "", fmt.Sprintf("(%s) -> %q", extCmd, ext))

	// ---- pipe through builtin ----
	// unix: sort; windows: sort.exe mangles LF-only input ("?"), findstr is
	// pipe-clean on both counts.
	pipeCmd := "printf 'b\\na\\n' | sort"
	if info.Os == "windows" {
		pipeCmd = "printf 'apple\\nbanana\\n' | findstr apple"
	}
	pipe := runSimple(c, ctx, pipeCmd)
	wantPipe := "a"
	if info.Os == "windows" {
		wantPipe = "apple"
	}
	check("builtin-pipe", pipe == wantPipe, fmt.Sprintf("(%s) -> %q", pipeCmd, pipe))

	// ---- files ----
	_, err = c.FileWrite(ctx, &workerv1.FileWriteRequest{Path: "ewtest/bin.dat", Content: []byte{0, 1, 2, 255}})
	check("file-write", err == nil, "")
	rd, err := c.FileRead(ctx, &workerv1.FileReadRequest{Path: "ewtest/bin.dat"})
	check("file-read-binary", err == nil && len(rd.Content) == 4 && rd.Content[3] == 255, "")
	ls, err := c.FileList(ctx, &workerv1.FileListRequest{Path: "ewtest"})
	check("file-list", err == nil && ls.IsDir && len(ls.Files) == 1, "")
	_, err = c.FileRead(ctx, &workerv1.FileReadRequest{Path: "../../etc/passwd"})
	check("file-containment", err != nil, "traversal refused")

	// ---- stdin (sort reads stdin on all platforms; cat is unix-only) ----
	catID := execIDOf(c, ctx, "sort")
	_, _ = c.JobStdin(ctx, &workerv1.JobStdinRequest{JobId: catID, Data: []byte("piped-stdin\n"), Close: true})
	catLines := waitForLines(c, ctx, catID, 10*time.Second)
	check("stdin-pipe", len(catLines) == 1 && catLines[0] == "piped-stdin", fmt.Sprintf("%v", catLines))

	// ---- kill (long-running external) ----
	longCmd := "sleep 60"
	if info.Os == "windows" {
		longCmd = "ping -n 60 127.0.0.1"
	}
	kid := execIDOf(c, ctx, longCmd)
	time.Sleep(500 * time.Millisecond)
	_, kerr := c.JobKill(ctx, &workerv1.JobKillRequest{JobId: kid})
	waitCtx2, cancel2 := context.WithTimeout(ctx, 15*time.Second)
	kw, _ := c.JobWait(waitCtx2, &workerv1.JobWaitRequest{JobId: kid, TimeoutMs: 10000})
	cancel2()
	check("kill-tree", kerr == nil && kw != nil && kw.State == "killed", fmt.Sprintf("state=%s (%s)", stateOf(kw), longCmd))

	fmt.Printf("\n%s  failures=%d\n", resultWord(), failures)
	if failures > 0 {
		os.Exit(1)
	}
}

// ---- helpers ----

func runSimple(c workerv1connect.WorkerServiceClient, ctx context.Context, cmd string) string {
	id := execIDOf(c, ctx, cmd)
	return waitForOutput(c, ctx, id, 10*time.Second)
}

func execIDOf(c workerv1connect.WorkerServiceClient, ctx context.Context, cmd string) string {
	e, err := c.Execute(ctx, &workerv1.ExecuteRequest{Command: cmd})
	if err != nil {
		fmt.Printf("FATAL Execute(%q): %v\n", cmd, err)
		os.Exit(1)
	}
	return e.JobId
}

func waitForOutput(c workerv1connect.WorkerServiceClient, ctx context.Context, id string, d time.Duration) string {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if lines := waitForLines(c, ctx, id, 0); len(lines) > 0 {
			return lines[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

func waitForLines(c workerv1connect.WorkerServiceClient, ctx context.Context, id string, d time.Duration) []string {
	deadline := time.Now().Add(d)
	for {
		r, err := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: id, Start: 0, End: 0})
		if err == nil && (r.Done || d == 0) {
			if len(r.Lines) > 0 {
				return r.Lines
			}
			if r.Done {
				return r.Lines
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		if d == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func linesOf(c workerv1connect.WorkerServiceClient, ctx context.Context, id, stream string) []string {
	r, err := c.JobOutput(ctx, &workerv1.JobOutputRequest{JobId: id, Start: 0, End: 0, Stream: stream})
	if err != nil {
		return nil
	}
	return r.Lines
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func stateOf(r *workerv1.JobWaitResponse) string {
	if r == nil {
		return "<nil>"
	}
	return r.GetState()
}
func exitOf(r *workerv1.JobWaitResponse) int32 {
	if r == nil {
		return -1
	}
	return r.GetExitCode()
}
func doneExit(d *workerv1.WatchJobResponse_Done) int32 {
	if d == nil {
		return -1
	}
	return d.ExitCode
}
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func resultWord() string {
	if failures == 0 {
		return "ALL PASS"
	}
	return "HAS FAILURES"
}
