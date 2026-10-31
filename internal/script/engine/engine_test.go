package engine

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	return New(Options{Dir: t.TempDir(), Env: os.Environ()})
}

func skipNoBash(t *testing.T, e *Engine) {
	t.Helper()
	if _, ok := e.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
}

// TestRunCollectsOutput: the basic embedder contract.
func TestRunCollectsOutput(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	var out, errOut bytes.Buffer
	code, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: "echo to-stdout\necho to-stderr >&2",
	}, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(out.String(), "to-stdout") {
		t.Fatalf("stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "to-stderr") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

// TestExitCodeIsReported: a non-zero exit is a code, not an error.
func TestExitCodeIsReported(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	code, err := e.Run(context.Background(), Spec{Lang: "bash", Code: "exit 7"}, nil, nil)
	if err != nil {
		t.Fatalf("a non-zero exit should not be an error: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit=%d, want 7", code)
	}
}

// TestKillStopsTheTree: Kill must be prompt and must not leave output behind.
func TestKillStopsTheTree(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	j, err := e.Start(context.Background(), Spec{
		Lang: "bash", Code: "echo begin\nsleep 30\necho never",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(joined(j.Snapshot()), "begin") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	j.Kill()
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Kill did not stop the job")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Kill took %v", d)
	}
	if st := j.Status(); st.State != StateKilled {
		t.Fatalf("state=%s", st.State)
	}
	if strings.Contains(joined(j.Snapshot()), "never") {
		t.Fatalf("killed job produced later output: %q", joined(j.Snapshot()))
	}
}

// TestTimeout: Spec.Timeout kills the job on its own.
func TestTimeout(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	code, _ := e.Run(context.Background(), Spec{
		Lang: "bash", Code: "sleep 30", Timeout: 300 * time.Millisecond,
	}, nil, nil)
	if code == 0 {
		t.Fatal("a timed-out job should not report success")
	}
}

// TestNoSessionStateLeaks is the contract an embedder most needs to know: two
// runs cannot share a variable or a working directory. easyscript has no
// sessions by design, and this test makes that explicit rather than a surprise.
func TestNoSessionStateLeaks(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)

	var out bytes.Buffer
	if _, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: "export LEAKED=yes; cd /tmp",
	}, &out, nil); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: `echo "LEAKED=${LEAKED:-unset}"`,
	}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "LEAKED=unset") {
		t.Fatalf("state leaked between runs: %q", out.String())
	}
}

// TestArgsReachTheScript: Spec.Args are the script's, not the interpreter's.
func TestArgsReachTheScript(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	var out bytes.Buffer
	if _, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: `echo "n=$# first=$1"`, Args: []string{"alpha", "beta"},
	}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "n=2") || !strings.Contains(out.String(), "first=alpha") {
		t.Fatalf("args did not reach the script: %q", out.String())
	}
}

// TestModeIsHonoured: a declared mode changes interpreter behaviour (node's
// cjs/esm), and an undeclared one is an error rather than a silent default.
func TestModeIsHonoured(t *testing.T) {
	e := testEngine(t)
	sup := e.Supported(context.Background())

	if _, ok := sup["node"]; ok {
		esm := "import os from 'node:os'\nconsole.log('ok', typeof os.platform())\n"
		if code, err := e.Run(context.Background(), Spec{Lang: "node", Code: esm, Mode: "esm"}, nil, nil); err != nil || code != 0 {
			t.Fatalf("esm: exit=%d err=%v", code, err)
		}
		if code, _ := e.Run(context.Background(), Spec{Lang: "node", Code: esm, Mode: "cjs"}, nil, nil); code == 0 {
			t.Fatal("a top-level import must fail under cjs")
		}
	}
	// python has no modes: asking for one is an error, never ignored.
	if _, ok := sup["python"]; ok {
		if _, err := e.Start(context.Background(), Spec{Lang: "python", Code: "print(1)", Mode: "esm"}); err == nil {
			t.Fatal("python + esm should be rejected")
		}
	}
}

// TestLanguagesAndResolve: the probe surface an embedder uses to build a picker.
func TestLanguagesAndResolve(t *testing.T) {
	e := testEngine(t)
	langs := e.Languages(context.Background())
	if len(langs) == 0 {
		t.Fatal("no languages")
	}
	found := false
	for _, l := range langs {
		if !l.Found {
			continue
		}
		found = true
		if l.Path == "" {
			t.Errorf("%s found without a path", l.ID)
		}
		v, err := e.Resolve(context.Background(), l.ID)
		if err != nil {
			t.Errorf("Resolve(%s): %v", l.ID, err)
		}
		if v.Path != l.Path {
			t.Errorf("Resolve(%s) path=%q, Languages said %q", l.ID, v.Path, l.Path)
		}
	}
	if !found {
		t.Skip("no interpreter installed")
	}
	if _, err := e.Resolve(context.Background(), "nope"); err == nil {
		t.Fatal("Resolve should reject an unknown language")
	}
}

// TestStdinReachesTheProgram: Spec.Stdin, which the daemon's API does not expose
// but an embedder reasonably needs.
func TestStdinReachesTheProgram(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	var out bytes.Buffer
	code, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: "read -r line; echo got:$line",
		Stdin: strings.NewReader("hello-stdin\n"),
	}, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(out.String(), "got:hello-stdin") {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
}

func joined(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Data)
	}
	return b.String()
}

// TestContextCancellationKills: an embedder that passes a cancellable ctx
// expects cancelling it to stop the job. This is the contract easyworker relies
// on (its jobs are cancelled when the request/session goes away), and getting it
// wrong meant `kill` silently did nothing.
func TestContextCancellationKills(t *testing.T) {
	e := testEngine(t)
	skipNoBash(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	j, err := e.Start(ctx, Spec{Lang: "bash", Code: "echo up\nsleep 30\necho never"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(joined(j.Snapshot()), "up") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context did not stop the job")
	}
	if strings.Contains(joined(j.Snapshot()), "never") {
		t.Fatalf("job kept running after cancel: %q", joined(j.Snapshot()))
	}
}

// TestNilEnvInheritsProcessEnv: Options.Env's zero value must mean "inherit this
// process's environment". Treating it as an empty environment strips PATH, and the
// child then cannot resolve its own interpreter — which is exactly how every job
// failed the first time this engine was embedded, because the embedder passed no
// Env. Every other test in this file passes os.Environ(), which is why none of
// them caught it.
func TestNilEnvInheritsProcessEnv(t *testing.T) {
	// Deliberately NO Env: this is the zero value a real embedder uses.
	e := New(Options{Dir: t.TempDir()})
	skipNoBash(t, e)
	var out bytes.Buffer
	code, err := e.Run(context.Background(), Spec{
		Lang: "bash", Code: `echo "PATH_SET=${PATH:+yes}"`,
	}, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "PATH_SET=yes") {
		t.Fatalf("PATH missing from the child environment: %q", out.String())
	}
}
