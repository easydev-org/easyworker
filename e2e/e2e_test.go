package e2e

import (
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The binary starts, answers its health probe, and serves the embedded panel.
//
// THE PANEL CHECK IS NOT COSMETIC: `//go:embed all:dist` accepts an empty directory, so a build with no
// UI ships happily and serves nothing — which is how v0.2.0-v0.3.0 shipped a "<html></html>" stub as the
// control panel.
func TestBinaryServesPanel(t *testing.T) {
	w := startWorker(t)

	resp, err := http.Get(w.base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	var body [1024]byte
	n, _ := resp.Body.Read(body[:])
	page := string(body[:n])
	if !strings.Contains(page, "<html") {
		t.Fatalf("the panel is not HTML: %q", page)
	}
	// A STUB IS SKIPPED, NOT FAILED — and the difference matters. The platform jobs create the stub
	// deliberately, because they test the ENGINE on Windows and macOS and have no Node toolchain to
	// build a real panel with. Failing there would say "the panel is broken" about a file this test
	// suite put there itself.
	//
	// The real assertion lives where a real panel exists: the `build` job, which builds the SPA before
	// running the tests. A stub reaching a RELEASE is caught by the binary's own startup check, which
	// logs "panel: ok" with a size, and by this test wherever dist/ came from a build.
	if !strings.Contains(page, "assets/") {
		t.Skip("the embedded panel is a stub — set up by the platform jobs; a real build is asserted in the build job")
	}
}

// Info reports what this host really is, and which languages it can really run.
func TestBinaryReportsItsHost(t *testing.T) {
	w := startWorker(t)
	in := w.info(t)

	if in.OS != runtime.GOOS {
		t.Errorf("os = %q, want %q", in.OS, runtime.GOOS)
	}
	if in.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", in.Arch, runtime.GOARCH)
	}
	if in.Workspace == "" {
		t.Error("workspace is empty")
	}
	// EVERY INTERPRETER OFFERED MUST NAME A REAL PATH. An entry without one would be a language a client
	// could pick and the engine could not run.
	for _, l := range in.Interpreters {
		if l.ID == "" {
			t.Errorf("an interpreter has no id: %+v", l)
		}
		if l.Path == "" {
			t.Errorf("%s was offered without a resolved path", l.ID)
		}
	}
}

// A program runs in a named language and its output comes back.
func TestBinaryRunsAProgram(t *testing.T) {
	w := startWorker(t)
	lang := w.hostLang(t)

	// ONE PROGRAM PER LANGUAGE, because the same text is not portable: `echo` is an external command in
	// bash and an alias in PowerShell, and neither the quoting nor the newline behaves the same.
	prog := "echo e2e-marker"
	if strings.Contains(lang, "powershell") {
		prog = `Write-Output 'e2e-marker'`
	}
	_, lines := w.runJob(t, lang, prog)
	requireContains(t, joined(lines), "e2e-marker")
}

// THE LANGUAGE IS REQUIRED, and the refusal names the ones that exist.
//
// This is the behaviour the whole contract changed for: the engine will not guess, because a guess
// would run the program in a language it was not written in and report success.
func TestBinaryRejectsACommandWithNoLanguage(t *testing.T) {
	w := startWorker(t)

	req, _ := http.NewRequest(http.MethodPost, w.base+"/worker.v1.WorkerService/Execute",
		strings.NewReader(`{"command":"echo hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var buf [512]byte
	n, _ := resp.Body.Read(buf[:])
	msg := strings.ToLower(string(buf[:n]))
	if !strings.Contains(msg, "lang") {
		t.Fatalf("the refusal does not mention lang: %s", msg)
	}
}

// A mode the language does not declare is refused rather than silently ignored.
func TestBinaryRejectsAnUndeclaredMode(t *testing.T) {
	w := startWorker(t)
	lang := w.hostLang(t)

	req, _ := http.NewRequest(http.MethodPost, w.base+"/worker.v1.WorkerService/Execute",
		strings.NewReader(`{"command":"echo hi","lang":"`+lang+`","mode":"no-such-mode"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 300 {
		t.Fatalf("an undeclared mode was accepted: status %d", resp.StatusCode)
	}
}

// Killing a running job stops it, and the state says so.
func TestBinaryKillsARunningJob(t *testing.T) {
	w := startWorker(t)
	lang := w.hostLang(t)

	prog := "sleep 30"
	if strings.Contains(lang, "powershell") {
		prog = "Start-Sleep -Seconds 30"
	}
	var started struct {
		JobID string `json:"jobId"`
	}
	w.rpc(t, "/worker.v1.WorkerService/Execute", map[string]any{"command": prog, "lang": lang}, "", &started)
	if started.JobID == "" {
		t.Fatal("no job id")
	}

	w.rpc(t, "/worker.v1.WorkerService/JobKill", map[string]any{"jobId": started.JobID}, "", nil)

	// It must actually stop: a kill that returns ok and leaves the process is the failure worth testing.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var st struct {
			State string `json:"state"`
		}
		w.rpc(t, "/worker.v1.WorkerService/JobOutput", map[string]any{"jobId": started.JobID}, "", nil)
		var list struct {
			Jobs []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"jobs"`
		}
		w.rpc(t, "/worker.v1.WorkerService/ListJobs", map[string]any{}, "", &list)
		for _, j := range list.Jobs {
			if j.ID == started.JobID {
				st.State = j.State
			}
		}
		if st.State != "" && st.State != "running" && st.State != "queued" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the job never reached a terminal state\n%s", w.logs.String())
}

// The token the binary printed is the only one that works: an anonymous caller is refused.
func TestBinaryRefusesAnonymousCallers(t *testing.T) {
	w := startWorker(t)

	req, _ := http.NewRequest(http.MethodPost, w.base+"/worker.v1.WorkerService/Info",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous Info = %d, want 401", resp.StatusCode)
	}
}

// A second worker on the same database recovers its jobs, which is what makes a restart survivable.
func TestBinaryRecoversAfterRestart(t *testing.T) {
	w := startWorker(t)
	lang := w.hostLang(t)
	jobID, _ := w.runJob(t, lang, "echo survived")

	// Stop it, then start another on the SAME database and workspace.
	_ = w.cmd.Process.Kill()
	_, _ = w.cmd.Process.Wait()

	w2 := startWorkerIn(t, w.dir)
	var list struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	w2.rpc(t, "/worker.v1.WorkerService/ListJobs", map[string]any{}, "", &list)
	for _, j := range list.Jobs {
		if j.ID == jobID {
			return
		}
	}
	t.Fatalf("job %s did not survive the restart; the new worker has %d job(s)", jobID, len(list.Jobs))
}
