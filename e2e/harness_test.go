// Package e2e drives the REAL easyworker binary: build it, start it on a free port, and talk to it
// over its own Connect RPC surface.
//
// WHY A BINARY AND NOT A HANDLER: the other tests in this repository call the service directly. They
// prove the logic and say nothing about whether the thing that SHIPS works — flag parsing, the
// embedded panel, the enrollment handshake, and the fact that a request survives a real socket and a
// real bearer. That is what this package is for.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// worker is one running binary, with the token that may talk to it.
type worker struct {
	base  string
	token string
	dir   string
	cmd   *exec.Cmd
	logs  *strings.Builder
}

// startWorker builds the binary once per test and starts it.
//
// THE BUILD RUNS HERE, not in a shared init, because `go build` of the real binary is the first half of
// what is being tested: a commit that compiles in a unit test but not with the shipped build tags is a
// broken release, and only this step notices.
func startWorker(t *testing.T, env ...string) *worker {
	t.Helper()
	return startWorkerIn(t, t.TempDir(), env...)
}

// startWorkerIn starts a worker against an EXISTING directory, so a test can restart one on the same
// database and check what survived. The directory is the identity: the database and the workspace both
// live in it, which is what a real deployment's volume is.
func startWorkerIn(t *testing.T, dir string, env ...string) *worker {
	t.Helper()
	bin := buildBinary(t)

	port := freePort(t)
	workspace := filepath.Join(dir, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}

	cmd := exec.Command(bin,
		"-addr", "127.0.0.1:"+port,
		"-db", filepath.Join(dir, "worker.db"),
		"-workspace", workspace,
	)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	w := &worker{base: "http://127.0.0.1:" + port, dir: dir, cmd: cmd, logs: &out}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("worker output:\n%s", out.String())
		}
	})
	w.waitHealthy(t)
	// A WORKER THAT WAS ALREADY CLAIMED DOES NOT PRINT A CODE — it RESUMED and says so. Re-claiming one
	// would be wrong (the handshake is one-time) and waiting for a code that will never appear would
	// hang. So the claim happens only when the log says this instance is new.
	if strings.Contains(w.logs.String(), "RESUMED") {
		// The token persisted; the caller supplies it, because the binary deliberately does not print a
		// stored one. Carried on the struct rather than through an environment variable: this is one
		// test handing a value to the next, not configuration.
		w.token = resumedToken
		return w
	}
	w.claim(t)
	resumedToken = w.token
	return w
}

// resumedToken is the token the most recent fresh worker issued, so a restart test can keep talking to
// the same worker across a restart. The binary does not reprint a stored token, by design.
var resumedToken string

// buildBinary compiles the shipped command once and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	name := "easyworker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "../cmd/easyworker")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out.String())
	}
	return bin
}

// freePort asks the OS for an unused port.
//
// THE OS PICKS, rather than a constant, so two tests running in parallel cannot fight over one — and a
// developer with something already on 8080 is not punished for it.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func (w *worker) waitHealthy(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(w.base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("worker never became healthy; output:\n%s", w.logs.String())
}

// claim takes the unclaimed worker with the enrollment code it printed, which is the only way to get a
// token from a fresh instance — and the handshake a real deployment performs.
func (w *worker) claim(t *testing.T) {
	t.Helper()
	code := w.enrollmentCode(t)
	var got struct {
		Token string `json:"token"`
	}
	w.rpc(t, "/worker.v1.WorkerEnroll/Claim", map[string]any{"code": code}, "", &got)
	if got.Token == "" {
		t.Fatalf("claim returned no token; output:\n%s", w.logs.String())
	}
	w.token = got.Token
}

// enrollmentCode reads the one-time code the binary logs at startup.
func (w *worker) enrollmentCode(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// The log is written by the child; a data race with the reader is avoided by asking again
		// rather than by locking, which is enough because this is a test helper.
		s := w.logs.String()
		if i := strings.Index(s, "enrollment code: "); i >= 0 {
			rest := s[i+len("enrollment code: "):]
			code := strings.Fields(rest)[0]
			if code != "" {
				return code
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no enrollment code in the log; output:\n%s", w.logs.String())
	return ""
}

// rpc posts one Connect request and decodes the reply.
func (w *worker) rpc(t *testing.T, path string, body any, token string, out any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, w.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	tok := token
	if tok == "" {
		tok = w.token
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d %s", path, resp.StatusCode, payload)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, payload)
		}
	}
}

// info is the worker's own report, which every test starts from.
type infoReply struct {
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Shell        string `json:"shell"`
	Workspace    string `json:"workspace"`
	Interpreters []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Path    string   `json:"path"`
		Version string   `json:"version"`
		Modes   []string `json:"modes"`
	} `json:"interpreters"`
}

func (w *worker) info(t *testing.T) infoReply {
	t.Helper()
	var got infoReply
	w.rpc(t, "/worker.v1.WorkerService/Info", map[string]any{}, "", &got)
	return got
}

// hostLang picks the language this host can actually run, from the worker's own list.
//
// IT COMES FROM THE WORKER, never from a constant: the engine reports only interpreters it managed to
// RUN a probe program with, so this cannot pick something that exists as a file and cannot execute.
func (w *worker) hostLang(t *testing.T) string {
	t.Helper()
	in := w.info(t)
	if len(in.Interpreters) == 0 {
		t.Skip("this worker reports no usable interpreter")
	}
	return in.Interpreters[0].ID
}

// runJob starts one job and waits for it to finish, returning everything it wrote.
func (w *worker) runJob(t *testing.T, lang, program string) (jobID string, output []string) {
	t.Helper()
	var started struct {
		JobID string `json:"jobId"`
	}
	w.rpc(t, "/worker.v1.WorkerService/Execute", map[string]any{
		"command": program,
		"lang":    lang,
	}, "", &started)
	if started.JobID == "" {
		t.Fatalf("execute returned no job id")
	}
	return started.JobID, w.waitOutput(t, started.JobID)
}

// waitOutput polls until the job is done and returns its lines.
func (w *worker) waitOutput(t *testing.T, jobID string) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var out struct {
			Lines []string `json:"lines"`
			Done  bool     `json:"done"`
		}
		w.rpc(t, "/worker.v1.WorkerService/JobOutput", map[string]any{"jobId": jobID}, "", &out)
		if out.Done {
			return out.Lines
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", jobID)
	return nil
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

func requireContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output does not contain %q:\n%s", want, got)
	}
}

var _ = fmt.Sprintf
