// Package jobsvc owns the job registry: every Execute registers a job,
// output is line-buffered for live streaming and persisted to sqlite for
// history (the memory ring is a bounded window; the DB is the authority).
// Jobs are cancellable (process-group kill via the shell runner).
package jobsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/easydev-org/easyworker/internal/script"
)

// Job states (matching the legacy vocabulary).
const (
	StateRunning = "running"
	StateDone    = "done"
	StateKilled  = "killed"
	StateFailed  = "failed"
)

// Job is one registered command run.
type Job struct {
	ID      string
	Command string
	// Namespace separates job families so the UI can group them: "user" for
	// caller commands, "computer" for the worker's own GUI-automation ops.
	Namespace string
	// Lang is the language the command was written in ("" = the runner default,
	// bash). Recorded so a client can label a job without guessing, and so a
	// mixed-language history still makes sense.
	Lang string
	// Mode is the language-specific execution mode the command asked for ("" =
	// the interpreter default). Recorded for the same reason as Lang.
	Mode       string
	State      string
	ExitCode   int32
	StartedAt  int64
	FinishedAt int64

	Stdout *script.LineBuffer
	Stderr *script.LineBuffer

	subMu sync.Mutex
	subs  []chan string
	// finished is set (under subMu) by finishSubs before the output channels are
	// closed. Subscribe checks it under the same lock, so a subscriber can never
	// register into the window between "subs closed" and "done closed" and get a
	// channel nobody will ever close (which would hang a WatchJob range).
	finished bool
	done     chan struct{}

	seq    atomic.Int64
	stdinW io.WriteCloser
	cancel context.CancelFunc
}

// Result is the terminal snapshot.
type Result struct {
	ExitCode int32
	Stdout   string
	Stderr   string
}

// Manager owns the job registry, the shared shell runner and the store.
type Manager struct {
	runner *script.Runner
	store  *Store

	memLines int // per-stream ring size
	replay   int // WatchJob replay cap

	mu   sync.Mutex
	jobs map[string]*Job
}

func NewManager(runner *script.Runner, store *Store, memLines, replay int) *Manager {
	if memLines <= 0 {
		memLines = 10000
	}
	if replay <= 0 {
		replay = 1000
	}
	m := &Manager{runner: runner, store: store, memLines: memLines, replay: replay, jobs: map[string]*Job{}}
	go m.retentionLoop()
	return m
}

func (m *Manager) retentionLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		m.store.Retention(retentionHours)
		m.Prune(time.Hour) // memory map follows the store
	}
}

// retentionHours is the history window (1 day per the sandbox-ephemeral
// policy; everything on emptyDir anyway).
const retentionHours = 24

// Namespaces: "user" for caller commands, "computer" for the worker's own
// GUI-automation operations.
const (
	NamespaceUser     = "user"
	NamespaceComputer = "computer"
)

// ExecOpts is what a caller asks for. It is a struct rather than a parameter list
// because the list had grown to six positional arguments, and because there is
// deliberately NO env field: the engine refuses per-job environments (an override
// is either an overlay to explain or a replacement that silently drops PATH). A
// caller that needs a variable puts it in the program.
type ExecOpts struct {
	// Namespace groups jobs in the UI ("" = NamespaceUser).
	Namespace string
	// Command is the program text.
	Command string
	// Workdir is relative to the workspace root ("" = the root).
	Workdir string
	// Lang is the interpreter id ("" = the runner's default).
	Lang string
	// Mode is a declared mode for that language ("" = the interpreter default).
	Mode string
	// TimeoutMs > 0 arms a wall-clock deadline: the job's process tree is killed
	// and it ends `killed`.
	TimeoutMs int32
}

// Execute starts a job in the user namespace and returns its id immediately.
func (m *Manager) Execute(ctx context.Context, opts ExecOpts) (string, error) {
	return m.ExecuteNS(ctx, opts)
}

// ExecuteNS starts a job with an explicit namespace (see the namespace consts).
func (m *Manager) ExecuteNS(ctx context.Context, opts ExecOpts) (string, error) {
	ns, command, workdir := opts.Namespace, opts.Command, opts.Workdir
	if ns == "" {
		ns = NamespaceUser
	}
	id := newID()
	// An os.Pipe, not an io.Pipe: os/exec passes an *os.File straight to the
	// child (no copying goroutine), whereas a non-*os.File stdin makes exec
	// spawn an io.Copy goroutine that never returns until the reader hits EOF —
	// which would deadlock every command, because the write end is only closed
	// when the job finishes.
	r, w, perr := os.Pipe()
	if perr != nil {
		return "", perr
	}

	jobCtx, cancel := context.WithCancel(context.Background())
	if opts.TimeoutMs > 0 {
		jobCtx, cancel = context.WithTimeout(jobCtx, time.Duration(opts.TimeoutMs)*time.Millisecond)
	}

	job := &Job{
		ID:        id,
		Command:   command,
		Namespace: ns,
		Lang:      opts.Lang,
		Mode:      opts.Mode,
		State:     StateRunning,
		StartedAt: time.Now().UnixMilli(),
		done:      make(chan struct{}),
		stdinW:    w,
		cancel:    cancel,
	}
	// ONE sequence source, assigned under the SAME lock as the live fanout.
	//
	// stdout and stderr arrive on two goroutines, each writing to its own
	// LineBuffer. Giving each buffer its own seq source (the obvious reading of
	// "nextSeq") meant the two raced: `echo first && sleep 1 && echo second &&
	// echo warn >&2` came out as first/warn/second, because stderr won a seq the
	// stdout line should have had. Assigning the seq inside the publish lock makes
	// the number mean "the order this line was published", which is exactly what
	// the replay sorts on and what persistence stores.
	emit := func(stream int) func(script.LineRec) {
		return func(rec script.LineRec) {
			// A blank line is a real line ("echo" prints an empty line) and MUST be
			// recorded; publishLine only skips FANNING OUT an empty string, because
			// a subscriber cannot tell it apart from a dropped event.
			seq := job.publishLine(rec.Line) // live fanout, ordered
			m.store.TryEnqueueLine(id, seq, stream, rec.Line)
		}
	}
	// The in-memory ring needs its own monotonically increasing number so
	// memSuffix can slice it; it is NOT the job-global order (that comes from
	// publishLine). One counter shared by both buffers keeps the two rings on the
	// same scale, which is what memSuffix compares across streams.
	var ringSeq atomic.Int64
	job.Stdout = script.NewLineBuffer(m.memLines, func() int64 { return ringSeq.Add(1) }, emit(StreamStdout))
	job.Stderr = script.NewLineBuffer(m.memLines, func() int64 { return ringSeq.Add(1) }, emit(StreamStderr))

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	if err := m.store.EnqueueJob(id, ns, command, opts.Lang, job.StartedAt); err != nil {
		// Non-fatal: history queries just won't see it.
		fmt.Printf("store: enqueue job %s: %v\n", id, err)
	}

	go func() {
		defer close(job.done)
		defer w.Close()
		defer job.finishSubs()

		// A panic anywhere in the interpreter/exec path must not take down the
		// whole worker (an unrecovered panic in ANY goroutine crashes the
		// process, dropping every other job and the sandbox). Recover, record
		// it as the job's failure, and keep serving.
		defer func() {
			if rec := recover(); rec != nil {
				job.State = StateFailed
				job.ExitCode = 1
				job.FinishedAt = time.Now().UnixMilli()
				job.Stderr.Write([]byte(fmt.Sprintf("worker: job panicked: %v\n", rec)))
				m.store.FinishJob(id, StateFailed, 1, job.FinishedAt)
			}
		}()

		script.Debugf("job %s: goroutine start (lang=%q cmd=%q)", id, job.Lang, command)
		// Output is an ordered stdout/stderr line stream and nothing else; there is
		// no dataflow graph to persist and no per-job environment to apply.
		res, err := m.runner.Run(jobCtx, script.Spec{
			Code:    command,
			Lang:    job.Lang,
			Mode:    job.Mode,
			Workdir: workdir,
			Stdin:   r,
			Stdout:  job.Stdout,
			Stderr:  job.Stderr,
		})
		script.Debugf("job %s: Run returned (err=%v, exit=%d, ctxErr=%v) mono=%d", id, err, res.ExitCode, jobCtx.Err() != nil, script.MonoMS())
		// Promote a trailing partial line (output without final \n) into the
		// history before signaling completion.
		_ = job.Stdout.FlushLine()
		_ = job.Stderr.FlushLine()

		state := StateDone
		switch {
		case err != nil:
			state = StateFailed
			job.ExitCode = int32(res.ExitCode)
			job.Stderr.Write([]byte("builtin shell error: " + err.Error() + "\n"))
		case jobCtx.Err() != nil:
			state = StateKilled
			job.ExitCode = int32(res.ExitCode)
		case res.ExitCode == 0:
			job.ExitCode = 0
		default:
			state = StateFailed
			job.ExitCode = int32(res.ExitCode)
		}
		job.State = state
		job.FinishedAt = time.Now().UnixMilli()
		m.store.FinishJob(id, state, job.ExitCode, job.FinishedAt)
		script.Debugf("job %s: state=%s exit=%d (done closing) mono=%d", id, state, job.ExitCode, script.MonoMS())
	}()

	return id, nil
}

// Get returns a live (in-memory) job by id.
func (m *Manager) Get(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job %q not found", id)
	}
	return j, nil
}

// List merges memory (running + recent) with the store (history).
func (m *Manager) List(limit int) []JobRow {
	return m.ListNS(limit, "")
}

// Record registers a COMPLETED job in `ns` purely for the history (the caller
// does not read its output through the job API). It does NOT execute anything:
// `command` is a human-readable label (e.g. "computer click"), which the
// ComputerService uses so GUI operations appear as jobs, separate from user
// jobs. Persisting only (no memory registration) keeps the registry from
// growing with every GUI op.
func (m *Manager) Record(ns, command string) {
	if ns == "" {
		ns = NamespaceUser
	}
	id := newID()
	now := time.Now().UnixMilli()
	if err := m.store.EnqueueJob(id, ns, command, "", now); err != nil {
		return
	}
	m.store.FinishJob(id, StateDone, 0, now)
}

// ListNS lists the newest jobs up to limit, optionally filtered to one
// namespace ("" = all).
func (m *Manager) ListNS(limit int, namespace string) []JobRow {
	if limit <= 0 {
		limit = 500
	}
	rows, err := m.store.QueryJobsNS(limit, namespace)
	if err != nil {
		rows = nil
	}
	byID := map[string]JobRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	m.mu.Lock()
	for _, j := range m.jobs {
		if _, ok := byID[j.ID]; ok {
			continue // store row may lag one flush; memory is fresher
		}
		byID[j.ID] = JobRow{
			ID: j.ID, Command: j.Command, Namespace: j.Namespace, Lang: j.Lang, State: j.State,
			ExitCode: j.ExitCode, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		}
	}
	m.mu.Unlock()
	out := make([]JobRow, 0, len(byID))
	for _, r := range byID {
		if namespace != "" && r.Namespace != namespace {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt > out[k].StartedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Prune drops finished jobs from MEMORY older than maxAge (the store has its
// own retention). The bounded ring already caps cost; this frees stdin
// pipes and cancel funcs.
func (m *Manager) Prune(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		if j.State != StateRunning && j.FinishedAt < cutoff {
			delete(m.jobs, id)
		}
	}
}

// readMerged returns the full (or stream-filtered) line history for a job:
// persisted lines plus the in-memory suffix not yet flushed (<=200ms lag).
//
// DEPRECATED for pagination paths (materializes everything); kept for
// whole-history consumers (tail computation, small jobs).
func (m *Manager) readMerged(id string, stream int) ([]script.LineRec, error) {
	persisted, _, err := m.store.QueryLines(id, stream)
	if err != nil {
		return nil, err
	}
	var maxSeq int64
	for _, r := range persisted {
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
	}
	j, err := m.Get(id)
	if err != nil {
		return persisted, nil // post-restart / pruned: DB is authoritative
	}
	var mem []script.LineRec
	appendStream := func(recs []script.LineRec) {
		for _, r := range recs {
			if r.Seq > maxSeq {
				mem = append(mem, r)
			}
		}
	}
	if stream == StreamStdout || stream < 0 {
		appendStream(j.Stdout.Recs())
	}
	if stream == StreamStderr || stream < 0 {
		appendStream(j.Stderr.Recs())
	}
	sort.Slice(mem, func(a, b int) bool { return mem[a].Seq < mem[b].Seq })
	return append(persisted, mem...), nil
}

// Store exposes the persistence layer (service layer queries history rows).
func (m *Manager) Store() *Store { return m.store }

// MergedRecs is the exported readMerged for tail computation.
func (m *Manager) MergedRecs(id string, stream int) ([]script.LineRec, error) {
	return m.readMerged(id, stream)
}

// memSuffix returns the job's in-memory ring lines with seq > afterSeq
// (stream -1 = both), in seq order. Empty when the job is out of memory.
func (m *Manager) memSuffix(id string, stream int, afterSeq int64) []script.LineRec {
	j, err := m.Get(id)
	if err != nil {
		return nil
	}
	var mem []script.LineRec
	appendStream := func(recs []script.LineRec) {
		for _, r := range recs {
			if r.Seq > afterSeq {
				mem = append(mem, r)
			}
		}
	}
	if stream == StreamStdout || stream < 0 {
		appendStream(j.Stdout.Recs())
	}
	if stream == StreamStderr || stream < 0 {
		appendStream(j.Stderr.Recs())
	}
	sort.Slice(mem, func(a, b int) bool { return mem[a].Seq < mem[b].Seq })
	return mem
}

// OutputLines paginates the (optionally stream-filtered) history. start may
// be negative (from the end). end is exclusive, <=0 means through the end.
//
// Pagination is pushed into SQL: only the requested page's persisted rows are
// materialized (plus the small in-memory tail), never the whole history.
func (m *Manager) OutputLines(id string, stream int, start, end int32) (lines []string, total int32, startLine int32, endLine int32, done bool, err error) {
	persistedCount, err := m.store.CountLines(id, stream)
	if err != nil {
		return nil, 0, 0, 0, false, err
	}
	maxSeq := int64(-1)
	if persistedCount > 0 {
		// The memory suffix starts after the newest persisted line. When
		// nothing is persisted yet the whole ring is the suffix.
		if ms, err := m.store.MaxSeq(id, stream); err == nil {
			maxSeq = ms
		}
	} else {
		maxSeq = 0
	}
	mem := m.memSuffix(id, stream, maxSeq)
	n := int32(persistedCount) + int32(len(mem))

	s := start
	if s < 0 {
		s = n + s
		if s < 0 {
			s = 0
		}
	}
	e := end
	if e <= 0 || e > n {
		e = n
	}
	if s > n {
		s = n
	}
	if e < s {
		e = s
	}

	out := make([]string, 0, e-s)
	// Persisted part of the window [s, min(e, pc)).
	pc := int32(persistedCount)
	if s < pc {
		hi := e
		if hi > pc {
			hi = pc
		}
		recs, rerr := m.store.QueryLinesRange(id, stream, int(s), int(hi-s))
		if rerr != nil {
			return nil, 0, 0, 0, false, rerr
		}
		for _, r := range recs {
			out = append(out, r.Line)
		}
	}
	// In-memory suffix part of the window [max(s,pc), e).
	if e > pc {
		lo := int(s - pc)
		if lo < 0 {
			lo = 0
		}
		for _, r := range mem[lo:min(int(e-pc), len(mem))] {
			out = append(out, r.Line)
		}
	}

	if j, gerr := m.Get(id); gerr == nil {
		done = j.State != StateRunning
	} else if row, ok, _ := m.jobState(id); ok {
		done = row.State != StateRunning
	}
	return out, n, s, e, done, nil
}

// jobState resolves a job from memory or the store.
func (m *Manager) jobState(id string) (JobRow, bool, error) {
	if j, err := m.Get(id); err == nil {
		return JobRow{ID: j.ID, Command: j.Command, State: j.State, ExitCode: j.ExitCode, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt}, true, nil
	}
	row, ok, err := m.store.QueryJob(id)
	return row, ok, err
}

// ReplayTail returns the last n merged lines (WatchJob replay). stream -1 =
// both. Reads the persisted tail backwards in SQL and stitches on the
// in-memory suffix — never materializes the whole history.
func (m *Manager) ReplayTail(id string, n int) []string {
	return m.ReplayTailStream(id, -1, n)
}

// ReplayTailStream is ReplayTail with a stream filter (0=stdout, 1=stderr).
func (m *Manager) ReplayTailStream(id string, stream, n int) []string {
	if n <= 0 {
		return nil
	}
	maxSeq := int64(0)
	if ms, err := m.store.MaxSeq(id, stream); err == nil {
		maxSeq = ms
	}
	mem := m.memSuffix(id, stream, maxSeq)
	need := n - len(mem)
	var recs []script.LineRec
	if need > 0 {
		persisted, err := m.store.QueryTail(id, stream, need)
		if err == nil {
			recs = persisted
		}
	}
	all := append(recs, mem...)
	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]string, 0, len(all))
	for _, r := range all {
		out = append(out, r.Line)
	}
	return out
}

// Dropped reports output lines lost to backpressure since process start:
// store-channel overflow (writer slower than producers) plus ring overflow
// on live jobs. 0 in healthy operation.
func (m *Manager) Dropped() int64 {
	n := m.store.Dropped()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		n += int64(j.Stdout.Dropped())
		n += int64(j.Stderr.Dropped())
	}
	return n
}

// Close shuts the manager down for a graceful stop: kill every running job's
// process tree (their finish records still commit), stop retention, and
// drain+close the store so the last ≤200ms batch is not lost.
func (m *Manager) Close() error {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		if j.State == StateRunning {
			j.Kill()
		}
	}
	// Give the killed jobs a moment to record their terminal state so the
	// finish commits before the store drains.
	deadline := time.Now().Add(3 * time.Second)
	for _, j := range jobs {
		select {
		case <-j.Done():
		case <-time.After(time.Until(deadline)):
			return m.store.Close()
		}
	}
	return m.store.Close()
}

// Subscribe returns the buffered live window (replay) plus a channel for
// subsequent output lines (nil channel if the job already finished). The
// replay merges stdout and stderr in sequence order; the channel carries lines
// from both streams. The channel closes on completion.
func (j *Job) Subscribe() (replay []string, ch chan string) {
	j.subMu.Lock()
	defer j.subMu.Unlock()
	recs := append(j.Stdout.Recs(), j.Stderr.Recs()...)
	sort.Slice(recs, func(a, b int) bool { return recs[a].Seq < recs[b].Seq })
	recsToLines := func(recs []script.LineRec) []string {
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Line)
		}
		return out
	}
	select {
	case <-j.done:
		return recsToLines(recs), nil
	default:
	}
	ch = make(chan string, 256)
	j.subs = append(j.subs, ch)
	return recsToLines(recs), ch
}

// publishLine assigns the job-global sequence number and fans the line out to
// live subscribers, both under one lock. Returning the seq lets the caller
// persist it with the same number the replay will sort on — two separate
// allocators here produced lines that disagreed with their own history.
func (j *Job) publishLine(line string) int64 {
	j.subMu.Lock()
	defer j.subMu.Unlock()
	seq := j.seq.Add(1)
	if line == "" {
		return seq
	}
	for _, ch := range j.subs {
		select {
		case ch <- line:
		default: // slow subscriber drops; JobOutput is the fallback
		}
	}
	return seq
}

func (j *Job) finishSubs() {
	j.subMu.Lock()
	defer j.subMu.Unlock()
	j.finished = true
	for _, ch := range j.subs {
		close(ch)
	}
	j.subs = nil
}

// Stdin writes to the job's stdin pipe.
func (j *Job) Stdin(data []byte, closeAfter bool) error {
	if len(data) > 0 {
		if _, err := j.stdinW.Write(data); err != nil {
			return err
		}
	}
	if closeAfter {
		return j.stdinW.Close()
	}
	return nil
}

// Kill cancels the job context; the runner kills the process tree.
func (j *Job) Kill() { j.cancel() }

// Done is the completion channel (for Wait).
func (j *Job) Done() <-chan struct{} { return j.done }

// Result returns the terminal snapshot (safe after Done closes; reading while
// running returns the in-flight values).
func (j *Job) Result() Result {
	return Result{
		ExitCode: j.ExitCode,
		Stdout:   j.Stdout.Tail(50),
		Stderr:   j.Stderr.Tail(50),
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
