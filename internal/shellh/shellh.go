// Package shellh runs commands through the easyscript engine.
//
// History: this used to wrap easysh (a flow-first shell whose every stream was an
// observable Flow, which gave the panel a dataflow graph for free). easyscript
// replaced it: it runs a program TEXT in any installed interpreter — bash today,
// python/node/perl/... as they become useful — with the same background/kill
// semantics, but with NO flow graph. Output is an ordered stdout/stderr line
// stream and nothing else, so JobGraph/JobFlow are gone from the worker.
//
// What that costs, stated plainly:
//   - no dataflow topology (which pipe carried what)
//   - no session state: easyscript runs each command as its own process, so a
//     `cd` or a variable does NOT carry into the next job. Callers must pass the
//     working directory on every run (jobsvc already resolves one per job).
//
// The public surface stays the same shape jobsvc already uses — Runner, Result,
// LineBuffer, LineRec, Debugf — so only the engine underneath changed.
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

	"github.com/easydev-org/easyscript/pkg/engine"
)

// DefaultLanguage is the language a job runs in when the caller does not pick
// one. bash is the closest thing to what this worker used to promise: POSIX sh
// and bash syntax, available on every image the worker ships in.
const DefaultLanguage = "bash"

// Runner executes command strings through the easyscript engine.
type Runner struct {
	workspace string

	mu   sync.RWMutex
	env  []string
	eng  *engine.Engine
	lang string
}

// New creates a Runner whose relative paths resolve against workspace.
func New(workspace string, env []string) *Runner {
	r := &Runner{workspace: workspace, env: env, lang: DefaultLanguage}
	r.eng = engine.New(engine.Options{Dir: workspace, Env: env})
	return r
}

// Workspace returns the root every relative path resolves against.
func (r *Runner) Workspace() string { return r.workspace }

// Language reports the language jobs run in by default.
func (r *Runner) Language() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lang
}

// SetLanguage changes the default language for future jobs. An unknown or
// unusable id is rejected here rather than at job time, so a misconfiguration
// fails loudly at startup instead of silently on the first command.
func (r *Runner) SetLanguage(id string) error {
	if _, err := r.eng.Resolve(context.Background(), id); err != nil {
		return err
	}
	r.mu.Lock()
	r.lang = id
	r.mu.Unlock()
	return nil
}

// Engine exposes the underlying engine for callers that need the probe (the
// worker's Info RPC reports which interpreters exist).
func (r *Runner) Engine() *engine.Engine { return r.eng }

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
	path := ""
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv[len("PATH="):]
			break
		}
	}
	if path == "" {
		path = os.Getenv("PATH")
	}
	add := strings.Join(dirs, string(os.PathListSeparator))
	r.env = nil
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		r.env = append(r.env, kv)
	}
	r.env = append(r.env, "PATH="+add+string(os.PathListSeparator)+path)
	// The engine holds its own copy of the environment, so rebuild it; the probe
	// cache is invalidated too, because a newly installed interpreter must be
	// discoverable without restarting the worker.
	r.eng = engine.New(engine.Options{Dir: r.workspace, Env: r.env})
}

// Result is the outcome of one run.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// RunWithEnv is Run with a per-job env overlay and explicit streams.
//
// The overlay is applied by rebuilding the engine's environment for this run
// only: easyscript deliberately has no per-job env override (an override is
// either an overlay to explain or a replacement that quietly drops PATH), so the
// worker composes the full environment itself and hands that over.
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

// run resolves the cwd and delegates to the easyscript engine.
func (r *Runner) run(ctx context.Context, command, workdir string, env []string, stdin io.Reader, stdout, stderr io.Writer) (Result, error) {
	dir := r.workspace
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			dir = filepath.Clean(workdir)
		} else {
			dir = filepath.Join(r.workspace, filepath.FromSlash(workdir))
		}
	}
	r.mu.RLock()
	lang := r.lang
	eng := r.eng
	r.mu.RUnlock()

	// A per-run engine keeps the env overlay local. It shares the probe cache
	// with the base engine only in spirit; constructing one per job is cheap
	// (a probe is cached per Engine, and detection happened at startup).
	if len(env) > 0 {
		eng = engine.New(engine.Options{Dir: r.workspace, Env: env})
	}

	code, err := eng.Run(ctx, engine.Spec{
		Lang:  lang,
		Code:  command,
		Dir:   dir,
		Stdin: stdin,
	}, stdout, stderr)
	return Result{ExitCode: code}, err
}

// KillAll reaps every process tree this runner has started (worker shutdown).
//
// easyscript tracks jobs individually, so this is a no-op: jobs are killed by
// their own Kill or by their context. Kept for API compatibility with callers
// that shut the worker down in one gesture.
func (r *Runner) KillAll() {}

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
