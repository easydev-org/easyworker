// Package lang describes the script interpreters easyscript can drive and
// probes which of them actually exist on this host.
//
// easyscript itself embeds nothing: every language is an external interpreter
// run as a child process. That single decision is what makes "any language with
// the same job lifecycle" possible — the daemon only needs to know three things
// per language: the executable, how to hand it a program on the command line,
// and how to force it to flush output immediately (otherwise live logs die).
package lang

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Lang is one supported scripting language.
type Lang struct {
	// ID is the stable, lowercase identifier used on the wire ("python", "node").
	ID string
	// Name is the human name ("Python", "Node.js").
	Name string
	// GOOS lists the operating systems this language exists on, using Go's own
	// names ("windows", "darwin", "linux", "android"). Empty means "everywhere",
	// which is what a portable interpreter (python, node, bash) wants.
	//
	// This is not a nicety: some languages are simply not a thing elsewhere. cmd
	// and vbscript only exist on Windows; swift only ships a real compiler for
	// darwin/linux/windows. Registering them unconditionally would advertise a
	// program the host cannot run, and a caller would only find out by failing.
	// The probe and the registry views filter on this, so on Linux those entries
	// are not merely "not found" — they are not offered.
	GOOS []string
	// Exts are the file extensions that map to this language, without the dot.
	Exts []string
	// FileExt is the extension for the TEMPORARY file a file-mode interpreter
	// (ProgFlag == "") gets, without the dot. Empty falls back to Exts[0].
	//
	// It exists because those two roles are not the same. Windows PowerShell is
	// file-mode and needs its script to be *.ps1, but ".ps1" already belongs to
	// PowerShell 7 for path-based language detection — and claiming it twice would
	// make ByExt ambiguous. So a language can take a temp-file suffix without
	// competing in extension detection.
	FileExt string
	// Candidates are the executable names to look for, in preference order
	// ("python3" before "python").
	Candidates []string
	// VersionArgs are the arguments that print the version.
	VersionArgs []string
	// VersionRe extracts the version from the output; group 1 is the version.
	// When nil, the first token containing a digit sequence is used.
	VersionRe *regexp.Regexp
	// InterpFlags are flags the interpreter always gets, placed right after the
	// executable: ["-u"] for python, ["--"] nothing for others.
	InterpFlags []string
	// ProgFlag is how the interpreter is told "the next argument is the program
	// text": "-c" (python/bash/sh), "-e" (node).
	// Empty means this interpreter can only read a script FILE, so the runner
	// writes the source to a temporary file and passes its path — the API never
	// sees a path either way.
	ProgFlag string
	// Argv0 is a placeholder inserted before the user's args when the
	// interpreter consumes the first one as $0 (bash -c, sh -c). Without it
	// `bash -c 'echo $1' a` would bind a to $0 and print nothing.
	Argv0 string
	// ArgsNeedSep inserts "--" between the script file and the user's arguments,
	// for interpreters that would otherwise parse them as their OWN options.
	// `swift f.swift --help` shows swift's help; `swift f.swift -- --help` passes
	// it to the script.
	ArgsNeedSep bool
	// Modes are the named execution modes this interpreter accepts, mapping a
	// user-facing name to the flags it implies. Only languages with a genuine
	// either/or choice define these (node: cjs|esm, bash: posix). An unknown
	// mode is rejected with the supported list, so a caller can never silently
	// get the wrong semantics.
	Modes map[string][]string
	// Env are extra environment variables that force line-buffered output.
	Env []string
	// Stub is a one-line program whose output proves the interpreter runs.
	Stub string
	// VersionOptional means "a missing version is normal for this interpreter"
	// (e.g. dash has no --version flag). The probe still verifies the stub runs,
	// so Found stays trustworthy — only Version may be empty.
	VersionOptional bool
}

// RunOpts are the per-job choices a caller may make, all language-neutral. What
// they MEAN is defined by the language (see Modes); anything the language does
// not support is rejected rather than silently ignored.
type RunOpts struct {
	// Mode picks a named execution mode (node: "cjs"/"esm", bash: "posix").
	// Empty means the interpreter default.
	Mode string
	// ExtraFlags are passed to the interpreter verbatim, before the program
	// (-X dev for python, -O for node, ...).
	ExtraFlags []string
	// Args are the SCRIPT's own arguments (sys.argv[1:], process.argv.slice(2),
	// $1.., @ARGV), i.e. what the program sees — not interpreter options.
	Args []string
}

// Argv builds the full interpreter command line for a program whose source is
// held in memory. Two shapes exist:
//
//	inline:  exe [interpFlags] [modeFlags] [extraFlags] <progFlag> SOURCE [argv0] [args...]
//	file:    exe [interpFlags] [modeFlags] [extraFlags] FILE   [args...]
//
// source is the program TEXT. filePath is only used when ProgFlag == "" (that
// interpreter can only read a file); the runner creates a temp file for it, so
// the API still only ever carries content, never a path.
func (l Lang) Argv(exe, source, filePath string, opts RunOpts) ([]string, error) {
	var modeFlags []string
	if opts.Mode != "" {
		f, ok := l.Modes[opts.Mode]
		if !ok {
			return nil, fmt.Errorf("%s does not support mode %q (supported: %s)",
				l.Name, opts.Mode, orNone(sortedKeys(l.Modes)))
		}
		modeFlags = f
	}
	argv := make([]string, 0, 8+len(opts.ExtraFlags)+len(opts.Args))
	argv = append(argv, exe)
	argv = append(argv, l.InterpFlags...)
	argv = append(argv, modeFlags...)
	argv = append(argv, opts.ExtraFlags...)
	if l.ProgFlag != "" {
		argv = append(argv, l.ProgFlag, source)
		if l.Argv0 != "" {
			argv = append(argv, l.Argv0)
		}
	} else {
		if filePath == "" {
			return nil, fmt.Errorf("%s: needs a script file", l.Name)
		}
		argv = append(argv, filePath)
	}
	if l.ArgsNeedSep && len(opts.Args) > 0 {
		argv = append(argv, "--")
	}
	argv = append(argv, opts.Args...)
	return argv, nil
}

// orNone renders an empty list as "(none)" for error messages.
func orNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}

// sortedKeys returns a language's mode names in a stable order for messages.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Version is the probed state of one language on this host.
type Version struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Exts []string `json:"exts"`
	// Modes are the named execution modes this interpreter accepts (node:
	// cjs, esm; bash: posix). Empty means "no mode choice"; a caller that sends
	// a mode for such a language is rejected.
	Modes   []string `json:"modes,omitempty"`
	Found   bool     `json:"found"`
	Path    string   `json:"path,omitempty"`
	Version string   `json:"version,omitempty"`
	// Error explains why an installed-looking interpreter is unusable (wrong
	// version output, refused to run, ...). Empty when Found is true.
	Error string `json:"error,omitempty"`
}

// registry is the static list of languages easyscript knows how to drive.
// Adding a language is one entry here — no other code changes.
var registry = []Lang{
	{
		ID: "python", Name: "Python", Exts: []string{"py"},
		Candidates:  []string{"python3", "python"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`),
		// -u is what makes print() reach us without waiting for a 4 KiB buffer.
		InterpFlags: []string{"-u"},
		ProgFlag:    "-c",
		Env:         []string{"PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8"},
		Stub:        "print('easyscript')",
	},
	{
		ID: "node", Name: "Node.js", Exts: []string{"js", "mjs", "cjs"},
		Candidates:  []string{"node", "nodejs"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`v?(\d+\.\d+\.\d+)`),
		ProgFlag:    "-e",
		// node -e puts the NEXT argument in process.argv[1] (where a script path
		// would go), so without a placeholder `slice(2)` would drop the first
		// user argument. Same reason bash needs one for $0.
		Argv0: "easyscript",
		// Node's two module systems are a genuine either/or, so they are modes:
		// --input-type applies exactly to the -e/eval shape we use.
		Modes: map[string][]string{
			"cjs": {"--input-type=commonjs"},
			"esm": {"--input-type=module"},
		},
		// console.log is line-buffered to a pipe on modern Node, but set
		// FORCE_COLOR off and NO_COLOR on so logs stay plain text.
		Env:  []string{"NO_COLOR=1"},
		Stub: "console.log('easyscript')",
	},
	{
		ID: "bash", Name: "Bash", Exts: []string{"sh", "bash"},
		Candidates:  []string{"bash"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`version (\d+\.\d+(?:\.\d+)?)`),
		ProgFlag:    "-c",
		// bash -c takes the NEXT word as $0, so without this placeholder the
		// first user arg would vanish into $0.
		Argv0: "easyscript",
		Modes: map[string][]string{"posix": {"--posix"}},
		Stub:  "echo easyscript",
	},
	{
		ID: "sh", Name: "POSIX sh", Exts: []string{},
		Candidates: []string{"dash", "sh"},
		// dash has no --version flag at all (it treats it as an illegal option),
		// so the version probe is the same one-liner as the stub: a successful
		// run is the proof of life. VersionRe stays nil, i.e. "best effort".
		VersionArgs: []string{"-c", "echo easyscript"},
		ProgFlag:    "-c",
		Argv0:       "easyscript",
		Stub:        "echo easyscript",
		// An interpreted shell has no version to report here; leave Version empty.
		VersionOptional: true,
	},

	{
		// PowerShell 7+ is the CROSS-PLATFORM one: Windows, macOS, Linux. Its
		// executable is "pwsh"; the ID says 7 so a caller can tell it apart from
		// the Windows-only 5.1 below without knowing the binary names.
		// .ps1 maps HERE, not to windowspowershell: on a host with both, the user
		// wants 7+, and ByExt takes the first match.
		ID: "powershell7", Name: "PowerShell 7+", Exts: []string{"ps1"},
		Candidates:  []string{"pwsh"},
		VersionArgs: []string{"-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()"},
		VersionRe:   regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`),
		// -File, NOT -Command. This is a real constraint of PowerShell, found by
		// the windows-latest CI job: `powershell -Command "<script>" one two`
		// appends "one two" to the SAME command string, so they run as extra
		// STATEMENTS rather than arriving in $args. A caller's script arguments
		// were therefore never passed — the script saw an empty $args and the
		// arguments were executed as PowerShell code.
		//
		// With -File the script is a file and everything after it is argv, which
		// is what every other language here does. The runner materialises a .ps1
		// (ProgFlag is empty), so multi-line scripts work too.
		InterpFlags: []string{"-NoProfile", "-NonInteractive", "-File"},
		ProgFlag:    "",
		Stub:        `Write-Output 'easyscript'`,
	},
	{
		// Windows PowerShell 5.1 (executable "powershell"): built into every
		// Windows install (no package manager, no WSL), which is exactly why it is
		// registered even though it is frozen and Windows-only. It claims no
		// extension: .ps1 belongs to powershell7 above.
		ID: "windowspowershell", Name: "Windows PowerShell", GOOS: []string{"windows"},
		// No Exts: ".ps1" belongs to powershell7 for path detection. FileExt gives
		// its temp script the .ps1 suffix it actually needs.
		Exts:        []string{},
		FileExt:     "ps1",
		Candidates:  []string{"powershell"},
		VersionArgs: []string{"-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()"},
		VersionRe:   regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`),
		// -File for the same reason as powershell7 above.
		InterpFlags: []string{"-NoProfile", "-NonInteractive", "-File"},
		ProgFlag:    "",
		Stub:        `Write-Output 'easyscript'`,
	},
	{
		// Nushell: structured data in the pipeline (tables, records, JSON in and
		// out) rather than text. Portable across Windows/macOS/Linux, which makes
		// it the one option here that gives a caller the SAME script on all three
		// — the thing people reach for PowerShell hoping to get.
		ID: "nu", Name: "Nushell", Exts: []string{"nu"},
		Candidates:  []string{"nu"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`),
		// --no-config-file: a user's config.nu must not change how a job behaves
		// (the same reason the PowerShell entries pass -NoProfile). Nothing else
		// is added that this registry has not confirmed nu accepts.
		InterpFlags: []string{"--no-config-file"},
		ProgFlag:    "-c",
		Stub:        `print easyscript`,
	},
	{
		// zsh is the DEFAULT shell on macOS (since Catalina) and is present on
		// most Linux images too. It matters because macOS ships bash 3.2 from
		// 2007 — no associative arrays, no mapfile — so "bash" on a Mac is not the
		// bash anyone means. zsh is what a Mac user is actually running.
		ID: "zsh", Name: "Zsh", GOOS: []string{"darwin", "linux"},
		Exts:        []string{"zsh"},
		Candidates:  []string{"zsh"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`zsh (\d+\.\d+(?:\.\d+)?)`),
		ProgFlag:    "-c",
		// zsh -c 'echo $1' a binds a to $0, exactly like bash. Argv0 fills the
		// placeholder so the first real argument lands in $1.
		Argv0: "zsh",
		Stub:  `echo easyscript`,
	},
	// ---- Swift -------------------------------------------------------------
	//
	// `swift script.swift` INTERPRETS the file directly: no package, no build
	// manifest, no separate compile step. It is a real scripting mode, not a
	// build. (I previously rejected swift on the grounds that it "only takes a
	// file" — which was wrong on its own terms, since AppleScript above is
	// registered on exactly that basis.)
	//
	// Two things a caller should still expect, which are properties of swift
	// rather than of this registry:
	//
	//   * Startup is slow by script-runner standards (the compiler front-end runs
	//     every time, roughly a second on a small file). It is not python-fast.
	//   * Foundation is available; AppKit/SwiftUI need the full macOS SDK, which a
	//     plain swift toolchain on Linux does not have. So the same script does
	//     NOT run everywhere — hence the per-platform GOOS list below.
	//
	// Registered for darwin/linux, NOT windows: Swift does publish a Windows
	// toolchain, but `binary` verification here would be guesswork, and a wrong
	// entry is worse than a missing one. Adding "windows" is a one-word change
	// once someone has actually run it there.
	{
		ID: "swift", Name: "Swift", GOOS: []string{"darwin", "linux"},
		Exts:        []string{"swift"},
		Candidates:  []string{"swift"},
		VersionArgs: []string{"--version"},
		VersionRe:   regexp.MustCompile(`Swift version (\d+\.\d+(?:\.\d+)?)`),
		// ArgsNeedSep: `swift f.swift --help` shows SWIFT's help. Without the "--"
		// a script's arguments would be eaten by the driver.
		ArgsNeedSep: true,
		ProgFlag:    "", // file mode: the runner materializes a .swift file
		Stub:        `print("easyscript")`,
	},
}

// On reports whether this language exists on the given GOOS. An empty GOOS list
// means "everywhere".
func (l Lang) On(goos string) bool {
	if len(l.GOOS) == 0 {
		return true
	}
	for _, o := range l.GOOS {
		if o == goos {
			return true
		}
	}
	return false
}

// All returns every language that exists on THIS host, in registry order.
//
// The registry itself is platform-agnostic: it describes languages, including
// ones that only make sense elsewhere (cmd on Windows, swift where a compiler
// ships). Filtering here means a caller never sees a language this host cannot
// possibly run, and does not have to reason about why an entry it saw is
// missing from the probe results.
func All() []Lang {
	out := make([]Lang, 0, len(registry))
	for _, l := range registry {
		if l.On(runtime.GOOS) {
			out = append(out, l)
		}
	}
	return out
}

// Get returns the language with the given id, if it exists on this host.
func Get(id string) (Lang, bool) {
	for _, l := range registry {
		if l.ID == id && l.On(runtime.GOOS) {
			return l, true
		}
	}
	return Lang{}, false
}

// ByExt returns the language registered for a file extension (no dot), if it
// exists on this host.
func ByExt(ext string) (Lang, bool) {
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	for _, l := range registry {
		if !l.On(runtime.GOOS) {
			continue
		}
		for _, e := range l.Exts {
			if e == ext {
				return l, true
			}
		}
	}
	return Lang{}, false
}

// ByPath guesses the language from a file name's extension.
func ByPath(path string) (Lang, bool) {
	return ByExt(strings.TrimPrefix(filepath.Ext(path), "."))
}

// Probe is the path/version detection layer. It is safe for concurrent use and
// caches each language's result (a probe shells out, so callers should not pay
// for it on every request). Call Invalidate to force a re-probe (e.g. after the
// user installs an interpreter).
type Probe struct {
	mu   sync.Mutex
	env  []string      // PATH (and friends) used for lookups
	dir  string        // working dir for the version/run probes
	ttl  time.Duration // how long a cached result stays fresh
	at   time.Time
	data []Version
}

// NewProbe builds a probe that resolves executables against `env` (use
// os.Environ() for the daemon's own environment) and runs version commands in
// `dir`.
func NewProbe(env []string, dir string) *Probe {
	return &Probe{env: env, dir: dir, ttl: 5 * time.Minute}
}

// Invalidate drops the cache so the next call re-probes.
func (p *Probe) Invalidate() {
	p.mu.Lock()
	p.at = time.Time{}
	p.data = nil
	p.mu.Unlock()
}

// Versions returns one entry per known language: Found tells the caller whether
// it can be used, Version what it reports, Path where it lives.
func (p *Probe) Versions(ctx context.Context) []Version {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.data != nil && time.Since(p.at) < p.ttl {
		return append([]Version(nil), p.data...)
	}
	// Only this host's languages: probing a Windows-only entry on Linux would
	// waste a lookPath and, worse, report an entry the caller could never use.
	langs := All()
	out := make([]Version, 0, len(langs))
	for _, l := range langs {
		out = append(out, p.probeOne(ctx, l))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	p.data = out
	p.at = time.Now()
	return append([]Version(nil), out...)
}

// Supported returns only the languages that were found, keyed by id.
func (p *Probe) Supported(ctx context.Context) map[string]Version {
	m := map[string]Version{}
	for _, v := range p.Versions(ctx) {
		if v.Found {
			m[v.ID] = v
		}
	}
	return m
}

// Resolve returns the usable Version for id, or an error naming the missing
// executable. It is what the job runner calls before starting a job.
func (p *Probe) Resolve(ctx context.Context, id string) (Version, error) {
	for _, v := range p.Versions(ctx) {
		if v.ID != id {
			continue
		}
		if !v.Found {
			l, _ := Get(id)
			msg := "not installed"
			if v.Error != "" {
				msg = v.Error
			}
			return v, fmt.Errorf("%s interpreter not available (%s: %s)", l.Name, strings.Join(l.Candidates, "/"), msg)
		}
		return v, nil
	}
	return Version{}, fmt.Errorf("unknown language %q", id)
}

// probeOne resolves the executable and asks it for its version. A successful
// version invocation is the proof that the interpreter actually works — a
// binary that exists but cannot run (wrong arch, broken shim) is reported as
// not found, with the reason in Error.
func (p *Probe) probeOne(ctx context.Context, l Lang) Version {
	v := Version{ID: l.ID, Name: l.Name, Exts: append([]string(nil), l.Exts...),
		Modes: sortedKeys(l.Modes)}
	exe, err := p.lookPath(l.Candidates)
	if err != nil {
		return v
	}
	v.Path = exe

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, l.VersionArgs...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	out, verr := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	// A language with no real version banner uses the stub as its probe; that
	// output ("easyscript") is not a version, so do not try to parse it.
	banner := text
	if l.VersionOptional {
		banner = ""
	}
	if verr != nil {
		// Some interpreters print the version to stderr and exit non-zero in odd
		// ways; keep the text but record the failure.
		if text == "" {
			v.Error = trimOneLine(verr.Error())
			return v
		}
	}
	if banner != "" {
		v.Version = extractVersion(l.VersionRe, banner)
	}
	// A version banner without a parsable number is not proof the interpreter
	// works (dash exits 0 for a stub but has no --version at all). Run the stub:
	// that is the real proof of life, and it is what a job will do.
	if !p.stubRuns(ctx, l, exe) {
		v.Error = "interpreter did not run the probe program"
		return v
	}
	if v.Version == "" && !l.VersionOptional {
		// Version banner present but unparsable, and this language is expected to
		// report one — treat as unusable so we never hand out a bad interpreter.
		v.Error = "could not parse a version from " + trimOneLine(text)
		return v
	}
	v.Found = true
	return v
}

// stubRuns executes the language's one-line stub and reports whether it exited 0
// and echoed the marker. This is the probe's real gate: an executable that
// exists but is broken (wrong arch, a shim that cannot find its runtime, a
// wrapper that refuses to run) fails here even if it resolved on PATH.
func (p *Probe) stubRuns(ctx context.Context, l Lang, exe string) bool {
	if l.Stub == "" {
		return true
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// A file-only interpreter (osascript for AppleScript) needs the stub in a
	// FILE. Passing "" made Argv fail with "needs a script file", so such a
	// language could never probe as found — the stub is exactly the case that has
	// to work for the probe to mean anything.
	filePath := ""
	cleanup := func() {}
	if l.ProgFlag == "" {
		f, ferr := os.CreateTemp(p.dir, "easyscript-probe-*"+stubSuffix(l))
		if ferr != nil {
			return false
		}
		filePath = f.Name()
		cleanup = func() { _ = os.Remove(filePath) }
		if _, werr := f.WriteString(l.Stub); werr != nil {
			f.Close()
			cleanup()
			return false
		}
		if cerr := f.Close(); cerr != nil {
			cleanup()
			return false
		}
	}
	defer cleanup()
	argv, err := l.Argv(exe, l.Stub, filePath, RunOpts{})
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = p.dir
	cmd.Env = append(append([]string(nil), p.env...), l.Env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "easyscript")
}

// stubSuffix gives the probe's temporary file an extension the interpreter
// recognises. AppleScript's osascript does not care, but swift does: `swift
// foo.swift` treats the argument as a script, while a suffix-less file makes it
// try to build a package.
func stubSuffix(l Lang) string {
	return "." + tempExt(l)
}

// tempExt is the extension a file-mode interpreter's temporary script gets:
// FileExt when set (the language needs a specific suffix but does not claim it for
// path detection), else the first advertised extension, else "txt".
func tempExt(l Lang) string {
	if l.FileExt != "" {
		return l.FileExt
	}
	if len(l.Exts) > 0 {
		return l.Exts[0]
	}
	return "txt"
}

// TempSuffix is the suffix (with the dot) a file-mode interpreter's temporary
// script gets. Exported because the job runner writes the file and must agree with
// the probe, which writes the stub.
func (l Lang) TempSuffix() string {
	return "." + tempExt(l)
}

// lookPath resolves the first executable candidate against the probe's PATH.
func (p *Probe) lookPath(candidates []string) (string, error) {
	pathEnv := pathFrom(p.env)
	for _, name := range candidates {
		if strings.ContainsAny(name, `/\`) {
			if isExecutableFile(name) {
				return name, nil
			}
			continue
		}
		for _, dir := range filepath.SplitList(pathEnv) {
			if dir == "" {
				dir = "."
			}
			cand := filepath.Join(dir, name)
			if isExecutableFile(cand) {
				return cand, nil
			}
			// Windows executables carry an extension. os/exec would resolve it at
			// exec time, but the PROBE does not exec: it stats a path to decide the
			// interpreter exists, and `C:\Windows\System32\WindowsPowerShell\v1.0\powershell`
			// is not a file — `powershell.exe` is. Without this, a Windows host
			// probed as having NO interpreters at all, which is exactly what the
			// windows-latest CI job caught the first time it ran.
			if runtime.GOOS == "windows" {
				for _, ext := range []string{".exe", ".cmd", ".bat"} {
					if isExecutableFile(cand + ext) {
						return cand + ext, nil
					}
				}
			}
		}
	}
	return "", os.ErrNotExist
}

// pathFrom returns the PATH value from an environment slice.
func pathFrom(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return kv[len("PATH="):]
		}
	}
	return os.Getenv("PATH")
}

// isExecutableFile reports whether p is a regular file we may execute.
func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	// Windows has no exec bit; a regular file is enough there.
	if os.PathSeparator == '\\' {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

// extractVersion pulls the version out of an interpreter's version banner.
func extractVersion(re *regexp.Regexp, out string) string {
	if re != nil {
		if m := re.FindStringSubmatch(out); len(m) > 1 {
			return m[1]
		}
	}
	// Fallback: the first token that looks like a dotted number.
	for _, tok := range strings.Fields(out) {
		tok = strings.TrimLeft(tok, "vV")
		if regexp.MustCompile(`^\d+\.\d+`).MatchString(tok) {
			return strings.TrimRight(tok, ",;")
		}
	}
	return trimOneLine(out)
}

// trimOneLine keeps diagnostics short enough for a JSON field.
func trimOneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
