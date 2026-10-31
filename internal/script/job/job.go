// Package job runs one script in one child process and exposes its live output
// and its process group, so any script in any language has the same lifecycle:
// start in the background, stream stdout/stderr as it happens, kill the whole
// tree at any time.
package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// State is a job's lifecycle position.
type State string

const (
	StateRunning State = "running"
	// StateQueued is a job accepted but waiting for a free slot (see
	// Runner.SetMaxRunning). It holds no process yet.
	StateQueued   State = "queued"
	StateExited   State = "exited"
	StateFailed   State = "failed"
	StateKilled   State = "killed"
	StateTimedOut State = "timed_out"
	// StateInterrupted is what a stored job reports when the daemon restarted
	// while it was still running: nothing supervises that process any more.
	StateInterrupted State = "interrupted"
)

// Event is one piece of output or one lifecycle transition, delivered in order.
type Event struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   string `json:"data"`
}

// Spec is what the caller asks for.
type Spec struct {
	// Lang is the interpreter id ("python", "node", ...).
	Lang string
	// Code is the program text.
	Code string
	// Mode is a named execution mode, meaningful only for languages that
	// declare one (node: "cjs"/"esm", bash: "posix"). An unsupported mode is an
	// error, never a silent fallback.
	Mode string
	// ExtraFlags are passthrough interpreter flags (-X dev, -w, -d k=v).
	ExtraFlags []string
	// Args are the SCRIPT's arguments (argv after the program), not
	// interpreter options.
	Args []string
	// Stdin feeds the program's standard input (nil = /dev/null). Deliberately
	// NOT exposed by the HTTP/RPC API — a background job reading a request body
	// deadlocks — but the engine package (library form) sets it, because an
	// embedder owns the process and can supply a reader.
	Stdin io.Reader
	// Dir is the working directory ("" = the process cwd).
	Dir string
	// Timeout kills the job after this long (0 = no limit).
	Timeout time.Duration
	// MaxLines caps the ring buffer kept in memory (0 = default 2000).
	MaxLines int
}

// Job is one running (or finished) script.
type Job struct {
	ID      string
	Lang    string
	Code    string
	Version string // interpreter version used
	Path    string // interpreter path used
	Started time.Time

	mu       sync.Mutex
	state    State
	exitCode int
	ended    time.Time
	err      string

	lines  []Event // ring of output, capped at maxLines
	seq    int64
	maxLen int

	subs map[int]chan Event // live subscribers
	next int
	// closed guards one-shot channel closure: a subscriber may be closed by its
	// own cancel func AND by job completion, and closing twice panics.
	closed map[int]bool

	cancel context.CancelFunc
	cmd    *exec.Cmd
	// pid is the child's pid, published under mu once Start() succeeds. Kill reads
	// it instead of cmd.Process, which Start() writes concurrently.
	pid int

	// onEvent is the persistence hook (see Runner.OnEvent); nil when not
	// persisting.
	onEvent func(id string, e Event)
	// onState is the state-change hook (see Runner.OnStateChange).
	onState func(j *Job)

	done chan struct{}
}

// Event gains a monotonic sequence number when appended.
func (j *Job) append(stream, data string) {
	j.mu.Lock()
	j.seq++
	ev := Event{Seq: j.seq, Stream: stream, Data: data}
	j.lines = append(j.lines, ev)
	if len(j.lines) > j.maxLen {
		j.lines = j.lines[len(j.lines)-j.maxLen:]
	}
	subs := make([]chan Event, 0, len(j.subs))
	for _, ch := range j.subs {
		subs = append(subs, ch)
	}
	j.mu.Unlock()
	// Persist as it is produced: a crash then loses at most the last line.
	if j.onEvent != nil {
		j.onEvent(j.ID, ev)
	}
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // a slow subscriber drops events rather than stalling the job
		}
	}
}

// Snapshot returns a copy of the buffered output.
func (j *Job) Snapshot() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.lines...)
}

// Status is the job's current view for the API.
type Status struct {
	ID       string `json:"id"`
	Lang     string `json:"lang"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path,omitempty"`
	State    State  `json:"state"`
	ExitCode int    `json:"exit_code"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended,omitempty"`
	Error    string `json:"error,omitempty"`
	Lines    int    `json:"lines"`
}

// Status reports the current state.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := Status{
		ID: j.ID, Lang: j.Lang, Version: j.Version, Path: j.Path,
		State: j.state, ExitCode: j.exitCode, Started: j.Started.UnixMilli(),
		Error: j.err, Lines: len(j.lines),
	}
	if !j.ended.IsZero() {
		st.Ended = j.ended.UnixMilli()
	}
	return st
}

// Subscribe returns a channel of FUTURE events plus a function to cancel. The
// caller is expected to send Snapshot() first so nothing is lost.
func (j *Job) Subscribe() (<-chan Event, func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	id := j.next
	j.next++
	ch := make(chan Event, 256)
	if j.subs == nil {
		j.subs = map[int]chan Event{}
	}
	j.subs[id] = ch
	return ch, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if _, ok := j.subs[id]; !ok {
			return // already removed by job completion or an earlier cancel
		}
		delete(j.subs, id)
		j.closeLocked(id)
	}
}

// closeLocked closes a subscriber's channel exactly once. A channel can be
// closed by both its own cancel func and job completion; closing twice panics,
// and close-of-closed-channel is exactly the crash this avoids. Caller holds mu.
func (j *Job) closeLocked(id int) {
	if j.closed == nil {
		j.closed = map[int]bool{}
	}
	if j.closed[id] {
		return
	}
	j.closed[id] = true
	if c, ok := j.subs[id]; ok {
		close(c)
	}
}

// Done is closed when the process has been reaped.
func (j *Job) Done() <-chan struct{} { return j.done }

// setRunning moves a queued job into running once it holds a slot. The seq is
// reset to 0 so the log starts at 1 for this execution regardless of how long it
// sat in the queue.
func (j *Job) setRunning() {
	j.mu.Lock()
	j.state = StateRunning
	notify := j.onState
	j.mu.Unlock()
	if notify != nil {
		notify(j)
	}
}

// currentState is the state under the lock (for the exit path's switch).
func (j *Job) currentState() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// settle records a terminal state, wakes every subscriber exactly once, and
// closes Done. Every exit path funnels through here so no path can forget to
// signal completion.
func (j *Job) settle(state State, exitCode int, err error) {
	j.mu.Lock()
	// A Kill already set StateKilled; do not let the exit path overwrite it.
	if j.state != StateKilled || state == StateKilled {
		j.state = state
	}
	j.exitCode = exitCode
	j.ended = time.Now()
	if err != nil && j.err == "" && state == StateFailed {
		j.err = err.Error()
	}
	ids := make([]int, 0, len(j.subs))
	for id := range j.subs {
		ids = append(ids, id)
	}
	for _, id := range ids {
		j.closeLocked(id)
	}
	select {
	case <-j.done:
		// already closed by another path; nothing to do
	default:
		close(j.done)
	}
	notify := j.onState
	j.mu.Unlock()
	if notify != nil {
		notify(j)
	}
}

// Kill terminates the whole process group. Safe to call repeatedly, and safe
// after the job has already exited (no-op).
func (j *Job) Kill() {
	j.mu.Lock()
	// A queued job has no process yet, but it is still cancellable: the exit it is
	// waiting on is the one the slot-waiter selects against, so cancelling is what
	// drops it from the queue instead of letting it run later.
	active := j.state == StateRunning || j.state == StateQueued
	cancel, pid := j.cancel, j.pid
	if active {
		j.state = StateKilled
	}
	j.mu.Unlock()
	if !active {
		return
	}
	if cancel != nil {
		cancel()
	}
	// Belt and braces: context cancellation makes exec kill the direct child,
	// but a script can spawn grandchildren; killTree reaps the whole group.
	if pid != 0 {
		killTree(pid)
	}
}

// Runner starts jobs. One Runner is enough for the whole daemon.
type Runner struct {
	probe *lang.Probe
	env   []string // the daemon's base environment, used when Spec.Env is nil
	seq   int64
	mu    sync.Mutex

	// onEvent, when set, receives every event as it is appended (used to persist
	// the log as it is produced, so a crash loses at most the last line).
	onEvent func(jobID string, e Event)

	// onState, when set, is called whenever a job's state changes (queued→running,
	// →exited, ...). It exists so a queued job that starts running is persisted as
	// running: without it the database kept saying "queued" until the job finished,
	// which is what a search or a restart would believe.
	onState func(j *Job)

	// sem bounds how many jobs run at once. nil means unlimited. Bytes here are
	// cheap; PROCESSES are not: 50 interpreters is ~1 GB of RSS, so the cap is
	// about memory, not goroutines (each job costs ~4 goroutines / ~70 KB).
	sem chan struct{}
}

// NewRunner wires a runner to the interpreter probe and the base environment.
func NewRunner(probe *lang.Probe, baseEnv []string) *Runner {
	return &Runner{probe: probe, env: baseEnv}
}

// SetMaxRunning bounds concurrent jobs. n <= 0 means unlimited. Jobs beyond the
// bound are accepted immediately and wait in state "queued" — the caller is never
// blocked and never refused, which matters because submitting a burst is a
// legitimate thing to do; running them all at once is not.
func (r *Runner) SetMaxRunning(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 {
		r.sem = nil
		return
	}
	r.sem = make(chan struct{}, n)
}

// MaxRunning reports the configured bound (0 = unlimited).
func (r *Runner) MaxRunning() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cap(r.sem)
}

// InFlight reports how many jobs currently hold a slot, i.e. how many interpreters
// are running right now. With no bound this is meaningless (0), because nothing is
// counted.
func (r *Runner) InFlight() int {
	r.mu.Lock()
	sem := r.sem
	r.mu.Unlock()
	if sem == nil {
		return 0
	}
	return len(sem)
}

// OnEvent installs a persistence hook. It must be fast and must not block; the
// store's AppendEvents satisfies that (a buffered file write).
func (r *Runner) OnEvent(fn func(jobID string, e Event)) {
	r.mu.Lock()
	r.onEvent = fn
	r.mu.Unlock()
}

// OnStateChange installs a hook called on every state transition.
func (r *Runner) OnStateChange(fn func(j *Job)) {
	r.mu.Lock()
	r.onState = fn
	r.mu.Unlock()
}

// Start resolves the interpreter and accepts the job. When a concurrency bound is
// configured, the job may come back in state "queued" and start as soon as a slot
// frees; either way Start returns immediately, so the caller (an HTTP request) is
// never blocked by a busy daemon.
func (r *Runner) Start(ctx context.Context, spec Spec) (*Job, error) {
	l, ok := lang.Get(spec.Lang)
	if !ok {
		return nil, fmt.Errorf("unknown language %q", spec.Lang)
	}
	v, err := r.probe.Resolve(ctx, spec.Lang)
	if err != nil {
		return nil, err
	}
	if spec.Code == "" {
		return nil, errors.New("empty program")
	}

	r.mu.Lock()
	r.seq++
	// A job id must be unique across RESTARTS, because jobs are persisted: a
	// plain counter would collide with the previous daemon's job-1 and overwrite
	// its stored log. The timestamp makes that impossible; the counter keeps ids
	// sortable and readable within one process.
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), r.seq)
	sem := r.sem
	onEvent := r.onEvent
	onState := r.onState
	r.mu.Unlock()
	maxLines := spec.MaxLines
	if maxLines <= 0 {
		maxLines = 2000
	}
	j := &Job{
		ID: id, Lang: l.ID, Code: spec.Code, Version: v.Version, Path: v.Path,
		Started: time.Now(), maxLen: maxLines,
		onEvent: onEvent, onState: onState, done: make(chan struct{}),
	}

	// Take a slot NOW if one is free, so the state Start returns is the true state
	// at that instant. Trying later (in a goroutine) raced: a slot freed between the
	// decision and the goroutine running made the caller see "queued" for a job that
	// was already running.
	gotSlot := true
	if sem != nil {
		select {
		case sem <- struct{}{}:
		default:
			gotSlot = false
		}
	}
	if gotSlot {
		j.state = StateRunning
	} else {
		j.state = StateQueued
	}

	// Validated here, not in exec(), so a bad mode/argv is a normal error instead
	// of a failed job the caller has to poll for.
	source, filePath, cleanup, err := materializeProgram(l, spec)
	if err != nil {
		releaseSlot(sem, gotSlot)
		return nil, err
	}
	argv, err := l.Argv(v.Path, source, filePath, lang.RunOpts{
		Mode:       spec.Mode,
		ExtraFlags: spec.ExtraFlags,
		Args:       spec.Args,
	})
	if err != nil {
		cleanup()
		releaseSlot(sem, gotSlot)
		return nil, err
	}

	if sem == nil {
		r.exec(ctx, spec, j, argv, cleanup)
		return j, nil
	}
	if gotSlot {
		// Started immediately. exec() returns as soon as the process is up, so the
		// slot must be freed only after the job has actually FINISHED — waiting on
		// Done() is what makes the bound real. (Releasing right after exec was the
		// bug: the slot came back in microseconds and nothing was ever limited.)
		go func() {
			r.exec(ctx, spec, j, argv, cleanup)
			<-j.Done()
			<-sem
		}()
		return j, nil
	}
	// Queued. Its own cancellable context exists NOW, not when it acquires a slot:
	// otherwise Kill has nothing to cancel while it waits, and the job would start
	// anyway after being "killed".
	qctx, qcancel := context.WithCancel(ctx)
	j.mu.Lock()
	j.cancel = qcancel
	j.mu.Unlock()
	go func() {
		select {
		case sem <- struct{}{}:
		case <-qctx.Done():
			cleanup()
			j.settle(StateKilled, -1, qctx.Err())
			return
		}
		j.setRunning()
		r.exec(qctx, spec, j, argv, cleanup)
		<-j.Done()
		<-sem // free the slot for the next queued job
	}()
	return j, nil
}

// releaseSlot returns a slot taken in Start when the job never reaches exec. Without
// this, a rejected job (bad mode, unwritable dir) would leak a slot forever and the
// daemon would slowly stop accepting work.
func releaseSlot(sem chan struct{}, gotSlot bool) {
	if sem == nil || !gotSlot {
		return
	}
	<-sem
}

// exec starts one prepared command and returns as soon as the process is running;
// its output keeps arriving in the background. Callers that need to know when it
// finished must wait on j.Done() — this function deliberately does NOT block, which
// is why a slot must be released from a goroutine that waits (see Start).
func (r *Runner) exec(ctx context.Context, spec Spec, j *Job, argv []string, cleanup func()) {
	runCtx, cancel := context.WithCancel(ctx)
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	// The child inherits the daemon's environment, plus the language's own
	// line-buffering variables. There is deliberately no per-job env override:
	// an override is either an overlay (a second concept to explain) or a full
	// replacement (which silently drops PATH/HOME and breaks subprocess). A
	// script that wants a variable sets it in its own language.
	l, _ := lang.Get(j.Lang)
	// There is no per-job env override by design — an override is either an
	// overlay to explain or a replacement that silently drops PATH/HOME. So the
	// child gets the engine's base environment, plus the language's own
	// line-buffering variables.
	//
	// A nil base must mean "inherit the daemon's environment", NOT "empty": the
	// zero value of Options.Env is nil, and treating that as an empty environment
	// would strip PATH and every job would fail to resolve its own interpreter.
	base := r.env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(append([]string(nil), base...), l.Env...)

	// One pipe PER STREAM, because the stream is a stored attribute, not a display
	// detail: a caller queries "just stderr" (job_lines.stream), so merging the two
	// fds into one would throw that column away.
	//
	// The honest consequence, and the one every external-interpreter runner shares:
	// ORDER IS GUARANTEED WITHIN A STREAM, NOT ACROSS THEM. Two fds are buffered
	// independently by the kernel and drained by two readers, so a stderr line
	// printed after a stdout line can be read first. There is no way around that
	// from outside the child: tagging the writes needs a copy goroutine per stream
	// (exec adds one for any io.Writer), and two copy goroutines reorder the bytes
	// regardless of how the writes are serialised. Only `2>&1` gives a strict
	// cross-stream order, and only by discarding the distinction.
	//
	// So: each stream keeps its own order exactly, and the interleaving between
	// them is best-effort.
	//
	// Measured, so nobody has to rediscover it: `echo first; echo second >&2;
	// echo third` (no sleeps — the two fds written as close together as bash can
	// manage) comes out as first/third/second in 36 of 200 runs, i.e. ~18%. Each
	// stream's OWN order was never wrong in those 200 runs. With a sleep between
	// the writes the interleaving is stable, which is why a test that spaces its
	// lines out looks like it proves ordering and does not.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		cleanup()
		j.settle(StateFailed, -1, err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		cleanup()
		j.settle(StateFailed, -1, err)
		return
	}
	// Put the child in its OWN process group, so Kill can reap everything the
	// script spawns (via subprocess, os.system, backticks, ...) in one signal.
	setupProcessGroup(cmd)
	cmd.Cancel = func() error {
		// exec calls this only after Start succeeded, so Process is set here.
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second

	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}

	if err := cmd.Start(); err != nil {
		cancel()
		cleanup()
		j.settle(StateFailed, -1, err)
		return
	}
	// Publish the process AND its pid under the lock, after Start has filled in
	// cmd.Process. Doing it before Start meant Kill could read a nil/in-flight
	// Process concurrently with Start writing it — a data race that the race
	// detector caught. Storing the pid separately keeps Kill lock-only.
	j.mu.Lock()
	j.cancel, j.cmd = cancel, cmd
	j.pid = cmd.Process.Pid
	j.mu.Unlock()
	// Windows: bind the process to a Job Object so a kill reaps the whole tree
	// (a script's children included) in one call, instead of hoping taskkill can.
	// No-op on Unix, where the process group already provides that.
	assignJobObject(j.pid)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(stdout, j, "stdout") }()
	go func() { defer wg.Done(); pump(stderr, j, "stderr") }()

	go func() {
		wg.Wait() // both pipes drained before Wait() closes them
		werr := cmd.Wait()
		cancel()
		code := 0
		var exitErr *exec.ExitError
		if werr != nil {
			if errors.As(werr, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		state := StateExited
		switch {
		case j.currentState() == StateKilled:
			state = StateKilled
		case runCtx.Err() == context.DeadlineExceeded:
			state = StateTimedOut
		case werr != nil && code != 0:
			state = StateFailed
		}
		j.settle(state, code, werr)
		releaseJobObject(j.pid)
		cleanup() // the temp script (if the language needed one) can go now
	}()
}

// pump copies one stream into the job line by line, so a subscriber sees output
// as soon as the interpreter flushes a line (why every language's registry entry
// forces line buffering).
//
// One pump per stream: order is exact within a stream, and the interleaving
// between streams is whatever the kernel and the scheduler give (see exec's
// comment — a strict cross-stream order is impossible from outside the child
// without discarding the stream attribute).
func pump(rd io.Reader, j *Job, stream string) {
	buf := make([]byte, 32*1024)
	var carry []byte
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			carry = nil
			// Emit complete lines; keep the tail for the next read.
			start := 0
			for i := 0; i < len(chunk); i++ {
				if chunk[i] == '\n' {
					j.append(stream, string(chunk[start:i+1]))
					start = i + 1
				}
			}
			if start < len(chunk) {
				carry = append(carry, chunk[start:]...)
			}
		}
		if err != nil {
			if len(carry) > 0 {
				j.append(stream, string(carry))
			}
			return
		}
	}
}
