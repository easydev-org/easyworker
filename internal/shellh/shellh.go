// Package shellh runs commands through the easysh shell engine. It replaces the
// old mvdan.cc/sh interpreter. easysh is a flow-first shell whose every stream
// is an observable Flow, so a job's output (and its intermediate pipes) come
// with a dataflow graph for free; it also accepts POSIX sh / bash syntax
// (assignments, ${...} expansion, $(...), f(){}, case ... ;; esac, until, ...),
// so AI-generated and pasted scripts run unmodified.
//
// The public surface is unchanged — Runner, LineBuffer, LineRec, Debugf — so
// jobsvc keeps working; only the engine underneath is easysh.
package shellh

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/easydev-org/easysh/pkg/exec"
	"github.com/easydev-org/easysh/pkg/flow"
)

// Runner executes command strings through the easysh engine.
type Runner struct {
	workspace string

	mu   sync.RWMutex
	env  []string
	proc *exec.Runner // the easysh runner: persistent session (vars/funcs/cwd)
}

// New creates a Runner whose relative paths resolve against workspace.
func New(workspace string, env []string) *Runner {
	r := &Runner{workspace: workspace, env: env}
	r.proc = exec.New(exec.Options{
		Dir:       workspace,
		Env:       env,
		Capture:   true,
		RingLines: 10000,
	})
	return r
}

// Workspace returns the root every relative path resolves against.
func (r *Runner) Workspace() string { return r.workspace }

// envList returns the current base environment.
func (r *Runner) envList() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.env...)
}

// AddPaths prepends directories to the base PATH of future jobs (idempotent).
// It backs the on-demand toolchain installer.
func (r *Runner) AddPaths(dirs ...string) {
	if len(dirs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, idx := "", -1
	for i, kv := range r.env {
		if strings.HasPrefix(kv, "PATH=") {
			cur, idx = kv[len("PATH="):], i
			break
		}
	}
	parts := strings.Split(cur, string(os.PathListSeparator))
	have := map[string]bool{}
	for _, p := range parts {
		have[p] = true
	}
	var prepend []string
	for _, d := range dirs {
		if d == "" || have[d] {
			continue
		}
		have[d] = true
		prepend = append(prepend, d)
	}
	if len(prepend) == 0 {
		return
	}
	newPath := strings.Join(append(prepend, parts...), string(os.PathListSeparator))
	if idx >= 0 {
		r.env[idx] = "PATH=" + newPath
	} else {
		r.env = append(r.env, "PATH="+newPath)
	}
	r.proc = exec.New(exec.Options{Dir: r.workspace, Env: r.env, Capture: true, RingLines: 10000})
}

// Result is the outcome of one run.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Graph is the job's dataflow (easysh-specific); nil when capture is off.
	Graph *flow.Registry
}

// RunWithEnv is Run with a per-job env overlay and explicit streams.
func (r *Runner) RunWithEnv(ctx context.Context, command, workdir string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (Result, error) {
	eff := r.envList()
	for k, v := range env {
		eff = append(eff, k+"="+v)
	}
	return r.run(ctx, command, workdir, eff, stdin, stdout, stderr)
}

// Run is RunWithEnv without a per-job env overlay.
func (r *Runner) Run(ctx context.Context, command, workdir string, stdin io.Reader, stdout, stderr io.Writer) (Result, error) {
	return r.run(ctx, command, workdir, r.envList(), stdin, stdout, stderr)
}

// run resolves the cwd and delegates to the easysh engine.
func (r *Runner) run(ctx context.Context, command, workdir string, env []string, stdin io.Reader, stdout, stderr io.Writer) (Result, error) {
	dir := r.workspace
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			dir = filepath.Clean(workdir)
		} else {
			dir = filepath.Join(r.workspace, filepath.FromSlash(workdir))
		}
	}
	res, err := r.proc.RunWithEnv(ctx, command, dir, env, stdin, stdout, stderr)
	out := Result{Graph: nil}
	if res != nil {
		out.ExitCode = res.ExitCode
		out.Graph = res.Registry
	}
	return out, err
}

// KillAll reaps every process tree this runner has started (worker shutdown).
func (r *Runner) KillAll() { r.proc.KillAll() }

// ── line buffering (unchanged from the mvdan runner; jobsvc depends on it) ──

// LineRec is one completed output line with its job-global sequence number.
type LineRec struct {
	Seq  int64
	Line string
}

// LineBuffer is an io.Writer that splits into lines as they are written. It is
// a bounded ring: when full the OLDEST lines are dropped (the sqlite store is
// the unbounded authority).
type LineBuffer struct {
	max     int
	nextSeq func() int64
	onLine  func(LineRec)

	mu        sync.Mutex
	recs      []LineRec
	cur       strings.Builder
	dropped   int
	pendingCR bool
}

// NewLineBuffer builds a line buffer holding at most max complete lines.
func NewLineBuffer(max int, nextSeq func() int64, onLine func(LineRec)) *LineBuffer {
	if max <= 0 {
		max = 10000
	}
	return &LineBuffer{max: max, nextSeq: nextSeq, onLine: onLine}
}

// Write splits p into lines (normalizing CRLF) as they are written.
func (lb *LineBuffer) Write(p []byte) (int, error) {
	var completed []LineRec
	lb.mu.Lock()
	complete := func() {
		rec := LineRec{Seq: lb.nextSeq(), Line: lb.cur.String()}
		lb.cur.Reset()
		lb.recs = append(lb.recs, rec)
		if len(lb.recs) > lb.max {
			lb.recs = lb.recs[1:]
			lb.dropped++
		}
		completed = append(completed, rec)
	}
	i := 0
	if lb.pendingCR && len(p) > 0 && p[0] == '\n' {
		i = 1
	}
	lb.pendingCR = false
	for ; i < len(p); i++ {
		switch p[i] {
		case '\r':
			complete()
			if i+1 < len(p) && p[i+1] == '\n' {
				i++
			} else if i+1 == len(p) {
				lb.pendingCR = true
			}
		case '\n':
			complete()
		default:
			lb.cur.WriteByte(p[i])
		}
	}
	lb.mu.Unlock()
	if lb.onLine != nil {
		for _, rec := range completed {
			lb.onLine(rec)
		}
	}
	return len(p), nil
}

// Recs returns a copy of the ring window (complete lines only).
func (lb *LineBuffer) Recs() []LineRec {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	out := make([]LineRec, len(lb.recs))
	copy(out, lb.recs)
	return out
}

// Dropped reports how many complete lines fell off the ring front.
func (lb *LineBuffer) Dropped() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.dropped
}

// FlushLine promotes a partial trailing line (output that never ended in \n)
// into the ring and returns it ("" when nothing pending).
func (lb *LineBuffer) FlushLine() string {
	lb.mu.Lock()
	if lb.cur.Len() == 0 {
		lb.mu.Unlock()
		return ""
	}
	rec := LineRec{Seq: lb.nextSeq(), Line: lb.cur.String()}
	lb.cur.Reset()
	lb.recs = append(lb.recs, rec)
	if len(lb.recs) > lb.max {
		lb.recs = lb.recs[1:]
		lb.dropped++
	}
	lb.mu.Unlock()
	if lb.onLine != nil {
		lb.onLine(rec)
	}
	return rec.Line
}

// Tail returns the last n lines joined with \n (plus any partial line).
func (lb *LineBuffer) Tail(n int) string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if n <= 0 || n > len(lb.recs) {
		n = len(lb.recs)
	}
	lines := make([]string, 0, n+1)
	for _, r := range lb.recs[len(lb.recs)-n:] {
		lines = append(lines, r.Line)
	}
	if lb.cur.Len() > 0 {
		lines = append(lines, lb.cur.String())
	}
	return strings.Join(lines, "\n")
}

// ── debug timing (kept for parity with the old runner) ──

var monoBase = time.Now()

// MonoMS is a monotonic millisecond clock (debug logs).
func MonoMS() int64 { return int64(time.Since(monoBase) / time.Millisecond) }

// Debugf prints to stderr when WORKER_DEBUG is set.
func Debugf(format string, args ...any) {
	if os.Getenv("WORKER_DEBUG") == "" {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	os.Stderr.WriteString(ts + " " + msg + "\n")
}
