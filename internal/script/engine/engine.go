// Package engine is the LIBRARY form of easyscript: the same job engine the
// daemon uses, without the HTTP/RPC surface, so another program can embed it.
//
// Design notes that matter to an embedder:
//
//   - No flow graph. easyscript exposes stdout/stderr as an ordered line stream
//     and nothing else. There is no dataflow topology to query, unlike easysh.
//   - No sessions. Every Run is independent: a job gets the dir and env you pass
//     and cannot affect the next one. A caller that needs continuity must pass
//     the state itself.
//   - No persistence. Where jobs are stored (or whether they are) is the
//     embedder's decision; this package only runs them and reports output.
//   - Kill is per job and takes the whole process tree with it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/easydev-org/easyworker/internal/script/job"
	"github.com/easydev-org/easyworker/internal/script/lang"
)

// State mirrors the job lifecycle. These are the same strings the daemon's API
// reports, so an embedder can pass them straight through.
type State = job.State

const (
	StateQueued      = job.StateQueued
	StateRunning     = job.StateRunning
	StateExited      = job.StateExited
	StateFailed      = job.StateFailed
	StateKilled      = job.StateKilled
	StateTimedOut    = job.StateTimedOut
	StateInterrupted = job.StateInterrupted
)

// Language is one interpreter the engine can drive.
type Language = lang.Version

// Event is one emitted line, tagged with its stream.
type Event = job.Event

// Status is a job's current state.
type Status = job.Status

// Language is a resolved interpreter (path + version + declared modes).
type LanguageInfo = lang.Lang

// Options configures an Engine.
type Options struct {
	// Dir is the default working directory for jobs ("" = process cwd).
	Dir string
	// Env is the base environment every job inherits (nil = the process's own).
	// A job cannot override it; a script that wants a variable sets it itself.
	Env []string
	// MaxRunning bounds concurrent jobs; extra jobs queue. 0 = unlimited.
	MaxRunning int
}

// Engine runs programs. It is safe for concurrent use.
type Engine struct {
	probe  *lang.Probe
	runner *job.Runner
	dir    string
}

// New builds an Engine and probes the interpreters on this host.
func New(opts Options) *Engine {
	probe := lang.NewProbe(opts.Env, opts.Dir)
	r := job.NewRunner(probe, opts.Env)
	if opts.MaxRunning > 0 {
		r.SetMaxRunning(opts.MaxRunning)
	}
	return &Engine{probe: probe, runner: r, dir: opts.Dir}
}

// Languages reports every interpreter the engine knows, with found/path/version.
// It shells out on the first call (then caches); Refresh forces a re-probe.
func (e *Engine) Languages(ctx context.Context) []Language {
	return e.probe.Versions(ctx)
}

// Supported reports only the usable interpreters, keyed by id.
func (e *Engine) Supported(ctx context.Context) map[string]Language {
	return e.probe.Supported(ctx)
}

// Refresh drops the probe cache so the next Languages call re-detects.
func (e *Engine) Refresh() { e.probe.Invalidate() }

// Detect maps a file name to a language ("" language when unknown).
func (e *Engine) Detect(path string) (string, bool) {
	l, ok := lang.ByPath(path)
	if !ok {
		return "", false
	}
	return l.ID, true
}

// Modes reports the named execution modes a language declares (e.g. node:
// cjs/esm). Empty means the language has no either/or choice.
func (e *Engine) Modes(language string) []string {
	l, ok := lang.Get(language)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l.Modes))
	for k := range l.Modes {
		out = append(out, k)
	}
	return out
}

// Spec describes one run.
type Spec struct {
	// Lang is the interpreter id ("bash", "python", ...).
	Lang string
	// Code is the program TEXT. The engine never takes a file path from the
	// caller; an interpreter that can only read a file gets a temp one it never
	// sees.
	Code string
	// Mode is a declared mode for that language ("" = the interpreter default).
	Mode string
	// ExtraFlags go to the interpreter (-X dev, -w, -d k=v).
	ExtraFlags []string
	// Args are the SCRIPT's arguments, not interpreter flags.
	Args []string
	// Dir overrides Options.Dir for this run ("" = the engine default).
	Dir string
	// Stdin, when non-nil, is the program's standard input. NOTE: the daemon's
	// HTTP/RPC API deliberately has no stdin — a background job fed by a request
	// body is a deadlock waiting to happen. An EMBEDDER is different: it owns the
	// process and can hand over a reader it controls.
	Stdin io.Reader
	// Timeout kills the job after this long (0 = no limit).
	Timeout time.Duration
	// MaxLines caps the in-memory ring of lines kept for Snapshot (0 = 2000).
	MaxLines int
}

// Job is a running (or finished) program.
type Job struct {
	j *job.Job
}

// Start launches a program and returns immediately. The job keeps running in the
// background; call Wait (or poll Done) to know when it finished.
//
// Lifetime: the job is started on its OWN context, so returning from Start does
// not kill it (an HTTP handler must not kill a background job on return). To stop
// it, either: call Job.Kill, give Spec.Timeout, or cancel the ctx you passed here
// — a small watcher turns that cancellation into a Kill. Both behaviours an
// embedder might want, without either surprising the other.
func (e *Engine) Start(ctx context.Context, spec Spec) (*Job, error) {
	dir := spec.Dir
	if dir == "" {
		dir = e.dir
	}
	// A MODE THE LANGUAGE DOES NOT DECLARE IS REFUSED.
	//
	// The proto promises this ("Sending a mode a language does not declare is an error, never a silent
	// fallback") and it was not true: the mode was recorded and passed through, so `bash` with
	// `mode: esm` ran as plain bash and reported success. A caller who mistyped a mode got a green
	// result from a run that ignored what they asked for — the exact failure the sentence exists to
	// prevent.
	//
	// Checked HERE because this is where a Spec becomes a job; a second check further in would be one
	// place to forget.
	if spec.Mode != "" {
		if _, ok := lang.Get(spec.Lang); !ok {
			// The engine does not import the caller-facing error type; naming the language is enough for
			// the caller to act on.
			return nil, fmt.Errorf("unknown language %q", spec.Lang)
		}
		declared := e.Modes(spec.Lang)
		found := false
		for _, m := range declared {
			if m == spec.Mode {
				found = true
				break
			}
		}
		if !found {
			if len(declared) == 0 {
				return nil, fmt.Errorf(
					"%s declares no modes, so mode %q cannot apply; omit it", spec.Lang, spec.Mode)
			}
			return nil, fmt.Errorf(
				"%s has no mode %q; it declares: %s", spec.Lang, spec.Mode, strings.Join(declared, ", "))
		}
	}
	j, err := e.runner.Start(context.Background(), job.Spec{
		Lang:       spec.Lang,
		Code:       spec.Code,
		Mode:       spec.Mode,
		ExtraFlags: spec.ExtraFlags,
		Args:       spec.Args,
		Dir:        dir,
		Stdin:      spec.Stdin,
		Timeout:    spec.Timeout,
		MaxLines:   spec.MaxLines,
	})
	if err != nil {
		return nil, err
	}
	// Bridge ctx to the job: a queued job already listens to its own context, and
	// for a running job Kill is the route. So add an explicit watcher that turns
	// ctx cancellation into a Kill — which is what callers expect when they pass
	// a cancellable context.
	jobWrap := &Job{j: j}
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				j.Kill()
			case <-j.Done():
			}
		}()
	}
	return jobWrap, nil
}

// Run starts a program and blocks until it finishes, returning the exit code.
// Output is written to out and errOut as lines arrive.
func (e *Engine) Run(ctx context.Context, spec Spec, out, errOut io.Writer) (int, error) {
	j, err := e.Start(ctx, spec)
	if err != nil {
		return -1, err
	}
	return j.Wait(out, errOut)
}

// ID is the job's identifier.
func (j *Job) ID() string { return j.j.ID }

// Status reports the current state.
func (j *Job) Status() Status { return j.j.Status() }

// Done is closed when the process has been reaped.
func (j *Job) Done() <-chan struct{} { return j.j.Done() }

// Snapshot returns the buffered lines (bounded by Spec.MaxLines).
func (j *Job) Snapshot() []Event { return j.j.Snapshot() }

// Kill terminates the job's whole process tree. Safe to call repeatedly and
// after completion (no-op).
func (j *Job) Kill() { j.j.Kill() }

// Wait blocks until the job finishes, copying lines to out/errOut as they
// arrive (nil writers are ignored), and returns its exit code.
//
// A non-zero exit is NOT an error — that is what the code is for, and callers
// treat `exit 7` very differently from "the interpreter could not be started".
// An error means the job could not run or was killed by something other than
// its own exit.
func (j *Job) Wait(out, errOut io.Writer) (int, error) {
	ch, cancel := j.j.Subscribe()
	defer cancel()

	// Replay what was already emitted before we subscribed.
	for _, e := range j.j.Snapshot() {
		writeEvent(out, errOut, e)
	}
	finish := func() (int, error) {
		st := j.j.Status()
		// Only a failure that never produced an exit code is an error; a script
		// that ran and exited non-zero did its job.
		if st.State == StateFailed && st.ExitCode < 0 {
			return st.ExitCode, errors.New(st.Error)
		}
		return st.ExitCode, nil
	}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return finish()
			}
			writeEvent(out, errOut, e)
		case <-j.j.Done():
			// Drain anything still queued, then report.
			for {
				select {
				case e, ok := <-ch:
					if !ok {
						return finish()
					}
					writeEvent(out, errOut, e)
				default:
					return finish()
				}
			}
		}
	}
}

func writeEvent(out, errOut io.Writer, e Event) {
	w := out
	if e.Stream == "stderr" {
		w = errOut
	}
	if w != nil {
		_, _ = io.WriteString(w, e.Data)
	}
}

// ErrUnknownLanguage is returned when the requested interpreter is not
// registered at all (as opposed to registered but unavailable).
var ErrUnknownLanguage = errors.New("unknown language")

// Resolve reports where a language lives and what version it reports, or an
// error explaining why it cannot be used on this host.
func (e *Engine) Resolve(ctx context.Context, language string) (Language, error) {
	if _, ok := lang.Get(language); !ok {
		return Language{}, fmt.Errorf("%w: %q", ErrUnknownLanguage, language)
	}
	return e.probe.Resolve(ctx, language)
}
