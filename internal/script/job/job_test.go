package job

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// testLang picks a language that is actually installed on this host, so the
// suite runs wherever it lands instead of hard-coding one interpreter.
func testLang(t *testing.T) string {
	t.Helper()
	p := lang.NewProbe(os.Environ(), t.TempDir())
	sup := p.Supported(context.Background())
	for _, id := range []string{"bash", "sh", "python", "node", "nu"} {
		if _, ok := sup[id]; ok {
			return id
		}
	}
	t.Skip("no supported interpreter on this host")
	return ""
}

// echoProgram returns a program in `id`'s language that prints the given lines.
func echoProgram(id string, lines ...string) string {
	switch id {
	case "python":
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("print(" + quote(l) + ")\n")
		}
		return b.String()
	case "node":
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("console.log(" + quote(l) + ")\n")
		}
		return b.String()
	default: // bash / sh
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("echo " + quote(l) + "\n")
		}
		return b.String()
	}
}

func quote(s string) string { return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'` }

// sleepThenPrint prints a line, sleeps, prints another — the shape a live log
// must expose in two visible steps.
func slowProgram(id string) string {
	switch id {
	case "python":
		return "import time\nprint('first', flush=True)\ntime.sleep(5)\nprint('second', flush=True)\n"
	case "node":
		return "console.log('first');\nsetTimeout(()=>{console.log('second')},5000);\n"
	default:
		return "echo first\nsleep 5\necho second\n"
	}
}

// TestLiveOutputArrivesBeforeExit is the core promise: the first line shows up
// while the job is still running — not buffered until the process ends.
func TestLiveOutputArrivesBeforeExit(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: slowProgram(id), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Kill()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(joined(j.Snapshot()), "first") {
			// "second" must NOT be there yet: the job is still sleeping.
			if strings.Contains(joined(j.Snapshot()), "second") {
				t.Fatalf("second line arrived too early (buffering not defeated): %q", joined(j.Snapshot()))
			}
			if j.Status().State != StateRunning {
				t.Fatalf("state=%s, expected running", j.Status().State)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("line 'first' never streamed while running: %q", joined(j.Snapshot()))
}

// TestSubscribeReceivesLive: a subscriber gets output as events, not on exit.
func TestSubscribeReceivesLive(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: slowProgram(id), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Kill()

	ch, cancel := j.Subscribe()
	defer cancel()
	select {
	case e := <-ch:
		if !strings.Contains(e.Data, "first") {
			t.Fatalf("first event = %+v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no live event within 4s")
	}
}

// TestExitCodeAndOutput covers the ordinary "run to completion" path.
func TestExitCodeAndOutput(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: echoProgram(id, "alpha", "beta"), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if got := j.Status(); got.ExitCode != 0 || got.State != StateExited {
		t.Fatalf("status=%+v", got)
	}
	out := joined(j.Snapshot())
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("output=%q", out)
	}
}

// TestKillStopsTheJob proves /kill works for any language: the process dies, the
// state becomes killed, and Done fires promptly instead of waiting 5s.
func TestKillStopsTheJob(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: slowProgram(id), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until it is really running.
	waitFor(t, 4*time.Second, func() bool { return strings.Contains(joined(j.Snapshot()), "first") })

	start := time.Now()
	j.Kill()
	select {
	case <-j.Done():
	case <-time.After(4 * time.Second):
		t.Fatalf("job did not die after Kill (waited %v)", time.Since(start))
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Kill took %v; it should be immediate", d)
	}
	st := j.Status()
	if st.State != StateKilled {
		t.Fatalf("state=%s want killed", st.State)
	}
	// The second line must never appear: the process was killed mid-sleep.
	if strings.Contains(joined(j.Snapshot()), "second") {
		t.Fatalf("job produced 'second' after being killed: %q", joined(j.Snapshot()))
	}
}

// TestKillIsIdempotent: killing a dead job must not panic.
func TestKillIsIdempotent(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: echoProgram(id, "x"), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	j.Kill()
	j.Kill()
}

// TestTimeout Kills: a runaway script is stopped by its deadline, with a state
// that says so.
func TestTimeoutKills(t *testing.T) {
	id := testLang(t)
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{
		Lang: id, Code: slowProgram(id), Dir: t.TempDir(), Timeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("timeout did not stop the job")
	}
	if st := j.Status(); st.State != StateTimedOut {
		t.Fatalf("state=%s want timed_out", st.State)
	}
}

// TestUnavailableLanguage: asking for an interpreter that is not installed is a
// clear error, not a silent failure.
func TestUnavailableLanguage(t *testing.T) {
	p := lang.NewProbe([]string{"PATH=" + t.TempDir()}, t.TempDir())
	r := NewRunner(p, os.Environ())
	_, err := r.Start(context.Background(), Spec{Lang: "python", Code: "print(1)"})
	if err == nil {
		t.Fatal("expected an error for a missing interpreter")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// TestUnknownLanguage.
func TestUnknownLanguage(t *testing.T) {
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	if _, err := r.Start(context.Background(), Spec{Lang: "brainfuck", Code: "+"}); err == nil {
		t.Fatal("expected an error")
	}
}

// TestFailedJobState: a non-zero exit is reported as failed with the code.
func TestFailedJobState(t *testing.T) {
	id := testLang(t)
	var code string
	switch id {
	case "python":
		code = "import sys; sys.exit(3)"
	case "node":
		code = "process.exit(3)"
	default:
		code = "exit 3"
	}
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: code, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	st := j.Status()
	if st.State != StateFailed || st.ExitCode != 3 {
		t.Fatalf("status=%+v want failed/3", st)
	}
}

// TestStderrSeparate: stderr is tagged so a UI can render it differently.
func TestStderrSeparate(t *testing.T) {
	id := testLang(t)
	var code string
	switch id {
	case "python":
		code = "import sys; print('o'); print('e', file=sys.stderr)"
	case "node":
		code = "console.log('o'); console.error('e')"
	default:
		code = "echo o; echo e >&2"
	}
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	j, err := r.Start(context.Background(), Spec{Lang: id, Code: code, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	var sawOut, sawErr bool
	for _, e := range j.Snapshot() {
		if e.Stream == "stdout" && strings.Contains(e.Data, "o") {
			sawOut = true
		}
		if e.Stream == "stderr" && strings.Contains(e.Data, "e") {
			sawErr = true
		}
	}
	if !sawOut || !sawErr {
		t.Fatalf("streams not separated: %+v", j.Snapshot())
	}
}

func joined(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Data)
	}
	return b.String()
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestStreamOrderAndLabels pins BOTH guarantees that actually hold:
//
//  1. order WITHIN each stream is exact — the lines of stdout come out in the
//     order stdout wrote them, likewise stderr;
//  2. every line carries the correct stream label, because the label is stored
//     (job_lines.stream) and a caller filters on it.
//
// What is deliberately NOT asserted is the interleaving BETWEEN streams. Two fds
// are buffered independently by the kernel and drained by two readers, so a
// stderr line printed after a stdout line may be read first. That is inherent to
// running an external interpreter: only `2>&1` gives a strict cross-stream order,
// and only by throwing away the label this test exists to check.
func TestStreamOrderAndLabels(t *testing.T) {
	id := testLang(t)
	if id != "bash" && id != "sh" {
		t.Skip("needs a shell")
	}
	r := NewRunner(lang.NewProbe(os.Environ(), t.TempDir()), os.Environ())
	for attempt := 0; attempt < 5; attempt++ {
		j, err := r.Start(context.Background(), Spec{
			Lang: id,
			// Several lines per stream, interleaved by construction.
			Code: "echo o1; echo e1 >&2; echo o2; echo e2 >&2; echo o3",
			Dir:  t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		<-j.Done()

		var stdout, stderr []string
		for _, e := range j.Snapshot() {
			d := strings.TrimSuffix(e.Data, "\n")
			switch e.Stream {
			case "stdout":
				stdout = append(stdout, d)
			case "stderr":
				stderr = append(stderr, d)
			default:
				t.Fatalf("unknown stream label %q", e.Stream)
			}
		}
		// Each stream keeps its own order exactly.
		if strings.Join(stdout, ",") != "o1,o2,o3" {
			t.Fatalf("attempt %d: stdout order = %v, want [o1 o2 o3]", attempt, stdout)
		}
		if strings.Join(stderr, ",") != "e1,e2" {
			t.Fatalf("attempt %d: stderr order = %v, want [e1 e2]", attempt, stderr)
		}
	}
}
