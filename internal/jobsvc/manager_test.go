package jobsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyworker/internal/script"
)

// shellLang returns a shell interpreter id for this host.
//
// ExecOpts.Lang is required now — there is no default — and every test in this
// file is written in shell syntax, so the honest thing is to name a shell and skip
// when the host has none.
func shellLang(t *testing.T) string {
	t.Helper()
	// Through the script package: easyscript's internal/lang is not importable
	// from another module, and Runner.Languages is the supported way to ask.
	r := script.New(t.TempDir())
	for _, id := range []string{"bash", "zsh", "sh"} {
		for _, l := range r.Languages(context.Background()) {
			if l.ID == id {
				return id
			}
		}
	}
	t.Skip("no shell interpreter on this host")
	return ""
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	ws := t.TempDir()
	dbPath := filepath.Join(ws, "jobs.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewManager(script.New(ws), store, 1000, 200), dbPath
}

func TestExecuteLifecycle(t *testing.T) {
	m, _ := newTestManager(t)

	// "Live" only means something if a line is published AFTER we subscribe. The
	// engine forks a real process, so anything short can finish before Execute's
	// caller even reaches Subscribe — and on a loaded CI runner even a 0.3s sleep
	// can elapse in the gap between Execute returning and Subscribe being called.
	// So: emit a line, then a long sleep, then the rest. That makes the FIRST line
	// deterministic (published immediately, will be in the replay) and the SECOND
	// unambiguously live (published seconds later, long after Subscribe).
	id, err := m.Execute(context.Background(), ExecOpts{
		Lang: shellLang(t), Command: "echo hi && sleep 3 && echo bye", Workdir: "", TimeoutMs: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	job, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}

	// One subscription. Subscribe replays history and then delivers what follows,
	// so a single channel carries everything: "hi" (printed immediately, from the
	// replay) then "bye" (printed seconds later, live). Subscribing once means no
	// leaked channels and one clear ordering to assert.
	replay, live := job.Subscribe()
	var got []string
	got = append(got, replay...)
	deadline := time.After(20 * time.Second)
	for len(got) < 2 {
		select {
		case line := <-live:
			got = append(got, line)
		case <-deadline:
			t.Fatalf("got %v, want [hi bye] (live channel stalled)", got)
		}
	}
	if got[0] != "hi" || got[1] != "bye" {
		t.Fatalf("got %v, want [hi bye]", got)
	}

	select {
	case <-job.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("job did not finish")
	}

	res := job.Result()
	if res.ExitCode != 0 {
		t.Errorf("exit = %d", res.ExitCode)
	}
	if res.Stdout != "hi\nbye" {
		t.Errorf("stdout tail = %q", res.Stdout)
	}

	lines, total, _, _, done, err := m.OutputLines(id, -1, 0, 0)
	if err != nil || total != 2 || len(lines) != 2 || lines[0] != "hi" || lines[1] != "bye" || !done {
		t.Errorf("history = %v (total %d done %v err %v)", lines, total, done, err)
	}
}

func TestExecuteNoTrailingNewline(t *testing.T) {
	m, _ := newTestManager(t)

	id, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "printf no-newline-here", Workdir: "", TimeoutMs: 0})
	job, _ := m.Get(id)
	<-job.Done()

	lines, total, _, _, _, _ := m.OutputLines(id, -1, 0, 0)
	if total != 1 || len(lines) != 1 || lines[0] != "no-newline-here" {
		t.Errorf("history = %v (total %d)", lines, total)
	}
}

func TestStreamFilter(t *testing.T) {
	m, _ := newTestManager(t)

	id, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "echo to-out && echo to-err >&2", Workdir: "", TimeoutMs: 0})
	job, _ := m.Get(id)
	<-job.Done()

	all, totalAll, _, _, _, _ := m.OutputLines(id, -1, 0, 0)
	if totalAll != 2 || len(all) != 2 {
		t.Fatalf("all = %v (total %d)", all, totalAll)
	}
	out, totalOut, _, _, _, _ := m.OutputLines(id, StreamStdout, 0, 0)
	if totalOut != 1 || len(out) != 1 || out[0] != "to-out" {
		t.Errorf("stdout = %v (total %d)", out, totalOut)
	}
	errs, totalErr, _, _, _, _ := m.OutputLines(id, StreamStderr, 0, 0)
	if totalErr != 1 || len(errs) != 1 || errs[0] != "to-err" {
		t.Errorf("stderr = %v (total %d)", errs, totalErr)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	ws := t.TempDir()
	dbPath := filepath.Join(ws, "jobs.db")

	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m1 := NewManager(script.New(ws), store1, 1000, 200)
	id, _ := m1.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "echo persist-me", Workdir: "", TimeoutMs: 0})
	job, _ := m1.Get(id)
	<-job.Done()

	// let the async line writer flush, then close
	time.Sleep(400 * time.Millisecond)
	store1.Close()

	// reopen: history must survive (emptyDir semantics: container restart)
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	m2 := NewManager(script.New(ws), store2, 1000, 200)

	lines, total, _, _, done, err := m2.OutputLines(id, -1, 0, 0)
	if err != nil || total != 1 || len(lines) != 1 || lines[0] != "persist-me" || !done {
		t.Errorf("post-restart history = %v (total %d done %v err %v)", lines, total, done, err)
	}
	row, ok, _ := store2.QueryJob(id)
	if !ok || row.State != StateDone {
		t.Errorf("post-restart row = %+v ok=%v", row, ok)
	}
}

func TestBootRecovery(t *testing.T) {
	ws := t.TempDir()
	dbPath := filepath.Join(ws, "jobs.db")

	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// simulate a job that was running when the worker died
	if err := store1.EnqueueJob("orphan1", NamespaceUser, "sleep forever", "", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	store1.Close()

	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	row, ok, _ := store2.QueryJob("orphan1")
	if !ok || row.State != StateFailed || row.ExitCode != 137 {
		t.Errorf("orphan = %+v ok=%v (want failed/137)", row, ok)
	}
}

func TestRetention(t *testing.T) {
	m, dbPath := newTestManager(t)

	id, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "echo old-job", Workdir: "", TimeoutMs: 0})
	job, _ := m.Get(id)
	<-job.Done()
	time.Sleep(400 * time.Millisecond)

	// age it beyond the window by rewriting finished_at directly
	if _, err := m.Store().db.Exec(`UPDATE jobs SET finished_at=? WHERE id=?`, time.Now().Add(-48*time.Hour).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if n := m.Store().Retention(24); n != 1 {
		t.Errorf("retention deleted %d jobs, want 1", n)
	}
	m.Prune(0) // memory map follows the store (retention loop does this hourly)
	_, total, _, _, _, _ := m.OutputLines(id, -1, 0, 0)
	if total != 0 {
		t.Errorf("retained lines after retention: %d", total)
	}
	_ = dbPath
}

func TestKillAndStdin(t *testing.T) {
	m, _ := newTestManager(t)

	// stdin round-trip
	id, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "cat", Workdir: "", TimeoutMs: 0})
	job, _ := m.Get(id)
	if err := job.Stdin([]byte("hello\n"), true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cat did not finish after stdin close")
	}
	lines, _, _, _, _, _ := m.OutputLines(id, -1, 0, 0)
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("lines = %v", lines)
	}

	// kill a sleeping job
	id2, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "sleep 60", Workdir: "", TimeoutMs: 0})
	job2, _ := m.Get(id2)
	time.Sleep(100 * time.Millisecond)
	job2.Kill()
	select {
	case <-job2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("kill did not finish the job")
	}
	if job2.State != StateKilled {
		t.Errorf("state = %s want killed", job2.State)
	}
}

func TestExecuteTimeoutKills(t *testing.T) {
	m, _ := newTestManager(t)
	// 200ms deadline on a 60s sleep: the worker must kill it.
	id, err := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "sleep 60", Workdir: "", TimeoutMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := m.Get(id)
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout did not kill the job")
	}
	if job.State != StateKilled {
		t.Errorf("state = %s want killed", job.State)
	}
}

func TestOutputLinesSQLPagination(t *testing.T) {
	m, _ := newTestManager(t)
	// 250 lines: enough to exercise page offsets against the store.
	id, _ := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "for i in $(seq 1 250); do echo line-$i; done", Workdir: "", TimeoutMs: 0})
	job, _ := m.Get(id)
	select {
	case <-job.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("job did not finish")
	}
	// wait for the writer to flush the persisted rows
	time.Sleep(300 * time.Millisecond)

	// A middle page by absolute offset.
	lines, total, start, end, done, err := m.OutputLines(id, -1, 100, 110)
	if err != nil {
		t.Fatal(err)
	}
	if total != 250 || start != 100 || end != 110 || !done {
		t.Fatalf("window meta: total=%d start=%d end=%d done=%v", total, start, end, done)
	}
	if len(lines) != 10 || lines[0] != "line-101" || lines[9] != "line-110" {
		t.Fatalf("page = %v", lines)
	}

	// Negative start = tail.
	lines, _, start, end, _, _ = m.OutputLines(id, -1, -3, 0)
	if start != 247 || end != 250 || len(lines) != 3 || lines[2] != "line-250" {
		t.Fatalf("tail page: start=%d end=%d lines=%v", start, end, lines)
	}

	// ReplayTail reads the persisted tail backwards.
	tail := m.ReplayTail(id, 2)
	if len(tail) != 2 || tail[1] != "line-250" {
		t.Fatalf("replay tail = %v", tail)
	}
}

// TestSubscribeAfterFinishNeverHangs locks the finish/Subscribe race: once a
// job's subscribers are closed, Subscribe must return a nil live channel — even
// in the window before the job's done channel is closed. Before the fix,
// Subscribe registered into an already-drained list and returned a channel
// nobody would ever close, hanging a WatchJob range forever.
func TestSubscribeAfterFinishNeverHangs(t *testing.T) {
	m, _ := newTestManager(t)
	id, err := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "echo done", Workdir: "", TimeoutMs: 0})
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	for i := 0; i < 200; i++ {
		replay, live := job.Subscribe()
		if live != nil {
			select {
			case _, ok := <-live:
				if ok {
					t.Fatalf("iter %d: live channel delivered a line after finish", i)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("iter %d: live channel never closed (would hang WatchJob)", i)
			}
		}
		_ = replay
	}
}

// TestRecordDoesNotExecuteAndNamespaces: Record must NOT run the label as a
// command (it would execute `computer find <selector>` — and inject arbitrary
// shell), and ListNS must filter by namespace.
func TestRecordDoesNotExecuteAndNamespaces(t *testing.T) {
	m, _ := newTestManager(t)
	// A label that would be a no-op-or-worse if executed; capture side effect.
	marker := filepath.Join(t.TempDir(), "ran")
	m.Record(NamespaceComputer, "computer find x; touch "+marker)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Record executed its label as a command (shell injection)")
	}

	// A user job and a computer record must be separable by namespace.
	id, err := m.Execute(context.Background(), ExecOpts{Lang: shellLang(t), Command: "echo hi", Workdir: "", TimeoutMs: 0})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := m.Get(id)
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	comp := m.ListNS(500, NamespaceComputer)
	if len(comp) != 1 || comp[0].Namespace != NamespaceComputer {
		t.Fatalf("computer namespace = %+v", comp)
	}
	user := m.ListNS(500, NamespaceUser)
	if len(user) == 0 {
		t.Fatal("user namespace empty")
	}
	for _, r := range user {
		if r.Namespace != NamespaceUser {
			t.Fatalf("user filter leaked %q", r.Namespace)
		}
	}
}

// TestOutputOrderAndStreamLabels pins what is actually guaranteed after merging
// an external interpreter's two fds into the worker's line history:
//
//  1. each stream's own order is exact (history for stream=stderr is e1,e2 in
//     that order, likewise stdout);
//  2. the stream label survives all the way to storage, which is what
//     JobOutput(stream: "stderr") filters on.
//
// The interleaving BETWEEN the streams is deliberately not asserted. It cannot be
// guaranteed: the engine runs a child process whose stdout and stderr are two
// separately buffered fds. (The engine's own TestStreamOrderAndLabels documents
// the same boundary.)
func TestOutputOrderAndStreamLabels(t *testing.T) {
	m, _ := newTestManager(t)
	id, err := m.Execute(context.Background(), ExecOpts{
		Lang:    shellLang(t),
		Command: "echo o1; echo e1 >&2; echo o2; echo e2 >&2; echo o3",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	<-job.Done()

	all, total, _, _, _, err := m.OutputLines(id, -1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(all) != 5 {
		t.Fatalf("all history total=%d lines=%v, want 5", total, all)
	}

	out, outTotal, _, _, _, err := m.OutputLines(id, StreamStdout, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if outTotal != 3 || strings.Join(out, ",") != "o1,o2,o3" {
		t.Fatalf("stdout history = %v (total %d), want [o1 o2 o3]", out, outTotal)
	}
	errs, errTotal, _, _, _, err := m.OutputLines(id, StreamStderr, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if errTotal != 2 || strings.Join(errs, ",") != "e1,e2" {
		t.Fatalf("stderr history = %v (total %d), want [e1 e2]", errs, errTotal)
	}
}
