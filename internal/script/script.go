// Package script is how the worker runs a command: it hands the program TEXT to
// the easyscript engine, which runs it in an installed interpreter as its own
// process group.
//
// There is no compatibility layer here. This package was written fresh against
// the easyscript API rather than adapted from the shell wrapper it replaces, so
// it carries no leftovers from the previous engine: no dataflow graph, no
// session state, no per-job environment overlay.
//
// Deliberate omissions and why:
//
//   - No env parameter. easyscript refuses per-job environments, and rightly so:
//     an "override" is either an overlay (a second concept for every caller) or a
//     replacement (which silently drops PATH and breaks child processes). A
//     caller that needs a variable adds a line to its program.
//   - No session. Each Run is its own process, so an `export` or a `cd` cannot
//     leak into the next command. Callers pass a working directory per run.
//   - No engine per job. The Runner owns ONE engine whose probe cache is shared,
//     so interpreter detection happens once and jobs stay cheap.
package script

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/easydev-org/easyworker/internal/script/engine"
)

// ErrNoLanguage means the caller did not say what language the command is written
// in. There is deliberately NO default.
//
// A default language cannot mean the same thing on two platforms, and that is not
// a detail — it is the whole problem. The same string "echo hello && ls" is valid
// bash, is a syntax error in Windows PowerShell 5.1 (no &&), and means something
// different again in PowerShell 7 (ls is an alias for Get-ChildItem). A worker
// that silently picks one would run a caller's command as a different language
// from the one it was written in, and report success.
//
// So the language is required. Callers that genuinely run on several platforms can
// read Info.interpreters and choose; the choice is theirs to make explicitly.
var ErrNoLanguage = errors.New("no language given")

// Runner executes commands. It is safe for concurrent use.
type Runner struct {
	root string
	eng  *engine.Engine
}

// New builds a Runner whose relative paths resolve against root.
//
// The engine is given NO Env, which means "inherit this process's environment".
// That is the only sensible default: a worker's jobs must see the PATH the worker
// itself resolves tools against, including anything an on-demand toolchain
// install added to the process PATH.
func New(root string) *Runner {
	return &Runner{
		root: root,
		eng:  engine.New(engine.Options{Dir: root}),
	}
}

// Root is the directory relative paths resolve against.
func (r *Runner) Root() string { return r.root }

// Engine exposes the engine, so callers can ask what this host can run.
func (r *Runner) Engine() *engine.Engine { return r.eng }

// Spec describes one run.
type Spec struct {
	// Code is the program text.
	Code string
	// Lang is the interpreter id, and it is REQUIRED. There is no default: a
	// command means different things in different languages (see ErrNoLanguage),
	// and guessing would run it as the wrong one.
	Lang string
	// Mode is a mode that language declares ("" = the interpreter default). A
	// mode the language does not declare is an error, never a silent default.
	Mode string
	// Workdir is relative to the Runner's root, or absolute ("" = the root).
	Workdir string
	// Stdin, when non-nil, is the program's standard input.
	Stdin io.Reader
	// Stdout/Stderr receive output as it is produced (nil = discard that stream).
	Stdout io.Writer
	Stderr io.Writer
}

// Result is the outcome of one run.
type Result struct {
	ExitCode int
	// Lang is the language actually used, resolved from the default when the
	// caller named none, so a job record can say what ran.
	Lang string
}

// Run executes a command and blocks until it finishes.
//
// A non-zero exit is a Result with that code, NOT an error: `exit 7` is a normal
// outcome, and callers treat it very differently from "the interpreter would not
// start". An error means the run could not happen at all (unknown language, bad
// mode, cancelled).
func (r *Runner) Run(ctx context.Context, spec Spec) (Result, error) {
	if strings.TrimSpace(spec.Lang) == "" {
		// Refuse rather than guess. The error names what IS available so a caller
		// can pick from real options instead of guessing at ids.
		ids := make([]string, 0, 4)
		for _, l := range r.Languages(ctx) {
			ids = append(ids, l.ID)
		}
		if len(ids) == 0 {
			return Result{}, fmt.Errorf("%w: this host has no interpreter installed", ErrNoLanguage)
		}
		return Result{}, fmt.Errorf("%w: state it explicitly (this host has: %s)",
			ErrNoLanguage, strings.Join(ids, ", "))
	}
	code, err := r.eng.Run(ctx, engine.Spec{
		Lang:  spec.Lang,
		Code:  spec.Code,
		Mode:  spec.Mode,
		Dir:   r.resolve(spec.Workdir),
		Stdin: spec.Stdin,
	}, spec.Stdout, spec.Stderr)
	return Result{ExitCode: code, Lang: spec.Lang}, err
}

// LanguageInfo is one runnable interpreter.
type LanguageInfo struct {
	ID      string
	Name    string
	Path    string
	Version string
	Modes   []string
}

// Languages reports what this host can run (found only), for the Info RPC.
func (r *Runner) Languages(ctx context.Context) []LanguageInfo {
	vers := r.eng.Languages(ctx)
	out := make([]LanguageInfo, 0, len(vers))
	for _, v := range vers {
		if !v.Found {
			continue
		}
		out = append(out, LanguageInfo{
			ID: v.ID, Name: v.Name, Path: v.Path, Version: v.Version, Modes: v.Modes,
		})
	}
	return out
}

// resolve turns a caller's workdir into a path under the root.
//
// It also refuses to escape the root: a caller naming "../../etc" would otherwise
// run with a cwd outside the workspace it was given, which is not something this
// worker should quietly allow.
func (r *Runner) resolve(workdir string) string {
	if workdir == "" {
		return r.root
	}
	if filepath.IsAbs(workdir) {
		return filepath.Clean(workdir)
	}
	joined := filepath.Join(r.root, filepath.FromSlash(workdir))
	// Compare with a separator so a sibling named like the root ("root2" vs
	// "root") does not pass for a child of it.
	if rel, err := filepath.Rel(r.root, joined); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return r.root
	}
	return joined
}

// Debugf prints to stderr when WORKER_DEBUG is set. It lives here because every
// caller that used to reach into the shell wrapper for it now reaches here.
func Debugf(format string, args ...any) {
	if os.Getenv("WORKER_DEBUG") == "" {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	os.Stderr.WriteString(msg + "\n")
}

var monoBase = time.Now()

// MonoMS is a monotonic millisecond clock for debug logs. It exists so a log line
// can be ordered against another without trusting the wall clock.
func MonoMS() int64 { return int64(time.Since(monoBase) / time.Millisecond) }
