package lang

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRegistryShape(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("registry is empty")
	}
	ids := map[string]bool{}
	exts := map[string]string{}
	for _, l := range all {
		if l.ID == "" || l.Name == "" || len(l.Candidates) == 0 {
			t.Fatalf("incomplete entry: %+v", l)
		}
		// Every language must be runnable: either it takes the program inline or
		// it reads a file (ProgFlag == "" is file mode, which the runner
		// materializes). A language with neither cannot run anything.
		if l.ProgFlag == "" && len(l.Exts) == 0 && l.FileExt == "" {
			t.Fatalf("%s: file-only interpreter needs an extension for its temp script", l.ID)
		}
		if ids[l.ID] {
			t.Fatalf("duplicate language id %q", l.ID)
		}
		ids[l.ID] = true
		for _, e := range l.Exts {
			if prev, ok := exts[e]; ok {
				t.Fatalf("extension %q claimed by both %q and %q", e, prev, l.ID)
			}
			exts[e] = l.ID
		}
	}
}

func TestLookupByIDAndExt(t *testing.T) {
	if l, ok := Get("python"); !ok || l.ID != "python" {
		t.Fatalf("Get(python) = %v %v", l, ok)
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("Get(nope) should fail")
	}
	// Only extensions for languages that exist on THIS host: ByExt filters by
	// platform, so asserting .zsh resolves on Windows would be asserting that the
	// platform filter does not work.
	cases := map[string]string{
		"main.py": "python", "app.js": "node", "run.sh": "bash",
		"b.nu": "nu", "z.ps1": "powershell7",
	}
	if _, ok := Get("zsh"); ok {
		cases["c.zsh"] = "zsh"
	}
	for path, want := range cases {
		l, ok := ByPath(path)
		if !ok || l.ID != want {
			t.Errorf("ByPath(%q) = %q %v, want %q", path, l.ID, ok, want)
		}
	}
	if _, ok := ByPath("README.md"); ok {
		t.Error("ByPath(README.md) should not resolve")
	}
}

// TestInvocationShape pins the argv each language gets, since a wrong flag is
// the kind of bug that only shows up at run time.
func TestInvocationShape(t *testing.T) {
	want := map[string][]string{
		"python": {"py", "-u", "-c", "PROG"},
		// node -e also takes the next word as process.argv[1].
		"node": {"node", "-e", "PROG", "easyscript"},
		// bash -c takes the next word as $0, so a placeholder is inserted; it is
		// what keeps the caller's first argument landing in $1.
		"bash": {"bash", "-c", "PROG", "easyscript"},
		// File mode: -File <path> and then the script's own arguments. -Command
		// would append them to the command string instead of passing them (see the
		// entry's comment), which is why this asserts -File.
		"powershell7": {"pwsh", "-NoProfile", "-NonInteractive", "-File", "PROG.ps1"},
	}
	for id, exp := range want {
		l, ok := Get(id)
		if !ok {
			t.Errorf("language %q is not registered on %s", id, runtime.GOOS)
			continue
		}
		// Inline languages take the program; file-mode ones take the path.
		source, filePath := "PROG", ""
		if l.ProgFlag == "" {
			source, filePath = "", "PROG.ps1"
		}
		got, err := l.Argv(exp[0], source, filePath, RunOpts{})
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if len(got) != len(exp) {
			t.Errorf("%s: argv=%v want %v", id, got, exp)
			continue
		}
		for i := range exp {
			if got[i] != exp[i] {
				t.Errorf("%s: argv=%v want %v", id, got, exp)
				break
			}
		}
	}
}

// TestArgvModesAndFlags pins the layering: interp flags, then mode flags, then
// extra flags, then the program, then the script's own args.
func TestArgvModesAndFlags(t *testing.T) {
	node, _ := Get("node")
	got, err := node.Argv("/usr/bin/node", "PROG", "", RunOpts{
		Mode: "esm", ExtraFlags: []string{"--no-warnings"}, Args: []string{"a", "b"},
	})
	// node gets an argv0 placeholder too: argv[1] is where a script path would
	// be, so process.argv.slice(2) is the user's arguments.
	want := []string{"/usr/bin/node", "--input-type=module", "--no-warnings", "-e", "PROG", "easyscript", "a", "b"}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("node esm argv=%v want %v", got, want)
	}

	// cjs is a different mode flag.
	got, _ = node.Argv("/usr/bin/node", "PROG", "", RunOpts{Mode: "cjs"})
	if !hasItem(got, "--input-type=commonjs") {
		t.Fatalf("node cjs argv=%v", got)
	}

	// bash's posix mode.
	bash, _ := Get("bash")
	got, _ = bash.Argv("/usr/bin/bash", "PROG", "", RunOpts{Mode: "posix", Args: []string{"x"}})
	want = []string{"/usr/bin/bash", "--posix", "-c", "PROG", "easyscript", "x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("bash posix argv=%v want %v", got, want)
	}
}

// TestArgvRejectsUnknownMode: a mode the language does not define is an error
// naming what IS supported — never a silent fallback.
func TestArgvRejectsUnknownMode(t *testing.T) {
	py, _ := Get("python")
	_, err := py.Argv("python3", "PROG", "", RunOpts{Mode: "esm"})
	if err == nil {
		t.Fatal("python has no esm mode; expected an error")
	}
	if !strings.Contains(err.Error(), "does not support mode") {
		t.Fatalf("unhelpful error: %v", err)
	}

	node, _ := Get("node")
	if _, err := node.Argv("node", "PROG", "", RunOpts{Mode: "bogus"}); err == nil {
		t.Fatal("bogus mode should be rejected")
	} else if !strings.Contains(err.Error(), "cjs") || !strings.Contains(err.Error(), "esm") {
		t.Fatalf("error should list the supported modes: %v", err)
	}
}

// TestArgvFileMode: a file-only interpreter gets the path we materialized.
func TestArgvFileMode(t *testing.T) {
	// Every current language is inline-capable, so exercise the file shape
	// directly: a Lang with no ProgFlag must require a file path.
	l := Lang{ID: "x", Name: "X", Exts: []string{"x"}}
	if _, err := l.Argv("x", "PROG", "", RunOpts{}); err == nil {
		t.Fatal("file-mode language without a path should fail")
	}
	got, err := l.Argv("x", "PROG", "/tmp/s.x", RunOpts{Args: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x", "/tmp/s.x", "a"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv=%v want %v", got, want)
	}
}

// TestProbeOnThisHost exercises the real detection layer: whatever is installed
// must be reported with a version, whatever is not must be reported as missing
// (never as a bogus success).
func TestProbeOnThisHost(t *testing.T) {
	p := NewProbe(os.Environ(), t.TempDir())
	vers := p.Versions(context.Background())
	if len(vers) != len(All()) {
		t.Fatalf("got %d entries, want %d", len(vers), len(All()))
	}
	for _, v := range vers {
		l, _ := Get(v.ID)
		if v.Found && v.Version == "" && !l.VersionOptional {
			t.Errorf("%s: found at %s but no version parsed", v.ID, v.Path)
		}
		if v.Found && v.Path == "" {
			t.Errorf("%s: found but no path", v.ID)
		}
		// When not found, Path/Version may still be filled in: they are the
		// diagnostic breadcrumbs that explain WHY it is unusable (resolved here,
		// reports this version, but the probe program failed). Only a "found"
		// entry is required to be self-consistent; a failed one carries Error.
		if !v.Found && v.Error == "" && v.Path != "" {
			t.Errorf("%s: has a path but is not found and gives no reason", v.ID)
		}
	}
	// Whatever we report as found must actually be runnable — cross-check by
	// resolving and running the language's stub.
	for _, v := range vers {
		if !v.Found {
			continue
		}
		l, _ := Get(v.ID)
		// A file-mode language (swift) needs its stub in a FILE; passing "" made
		// Argv return an error, which this loop ignored, leaving argv EMPTY and
		// panicking on argv[0]. That is the same trap the probe itself had, so the
		// test now materialises the file the same way the probe does.
		filePath := ""
		if l.ProgFlag == "" {
			f, ferr := os.CreateTemp(t.TempDir(), "stub-*"+stubSuffix(l))
			if ferr != nil {
				t.Fatalf("%s: %v", v.ID, ferr)
			}
			filePath = f.Name()
			if _, werr := f.WriteString(l.Stub); werr != nil {
				f.Close()
				t.Fatalf("%s: %v", v.ID, werr)
			}
			if cerr := f.Close(); cerr != nil {
				t.Fatalf("%s: %v", v.ID, cerr)
			}
		}
		argv, aerr := l.Argv(v.Path, l.Stub, filePath, RunOpts{})
		if aerr != nil || len(argv) == 0 {
			t.Errorf("%s: cannot build an invocation for the stub: %v", v.ID, aerr)
			continue
		}
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			t.Errorf("%s: reported found but the stub failed: %v (%s)", v.ID, err, out)
			continue
		}
		if !contains(string(out), "easyscript") {
			t.Errorf("%s: stub output %q does not echo the marker", v.ID, out)
		}
	}
	t.Logf("supported on this host: %v", supportedIDs(vers))
}

// TestProbeCachesAndInvalidates proves the probe does not shell out on every
// call (a probe is an exec, so a hot /api/languages must not fork).
func TestProbeCachesAndInvalidates(t *testing.T) {
	p := NewProbe(os.Environ(), t.TempDir())
	a := p.Versions(context.Background())
	b := p.Versions(context.Background())
	if &a[0] == &b[0] {
		t.Log("copies share no backing array (expected)")
	}
	if len(a) != len(b) {
		t.Fatal("cache returned different lengths")
	}
	p.Invalidate()
	c := p.Versions(context.Background())
	if len(c) != len(a) {
		t.Fatal("re-probe changed the language count")
	}
}

// TestProbeFindsNothingWithEmptyPath proves a missing interpreter is reported as
// missing rather than crashing or claiming success.
func TestProbeFindsNothingWithEmptyPath(t *testing.T) {
	p := NewProbe([]string{"PATH=" + t.TempDir()}, t.TempDir())
	for _, v := range p.Versions(context.Background()) {
		if v.Found {
			t.Fatalf("%s reported found with an empty PATH (%s)", v.ID, v.Path)
		}
	}
	if _, err := p.Resolve(context.Background(), "python"); err == nil {
		t.Fatal("Resolve should fail when nothing is installed")
	}
}

// TestProbeUnusableBinary: a file that exists but cannot run must not be
// reported as a working interpreter.
func TestProbeUnusableBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub is unix-only")
	}
	dir := t.TempDir()
	// A "python3" that is not a real interpreter: it exits 1 with noise.
	fake := filepath.Join(dir, "python3")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewProbe([]string{"PATH=" + dir}, dir)
	for _, v := range p.Versions(context.Background()) {
		if v.ID == "python" && v.Found {
			t.Fatalf("a broken python3 was reported as usable: %+v", v)
		}
	}
}

func supportedIDs(vers []Version) []string {
	var out []string
	for _, v := range vers {
		if v.Found {
			out = append(out, v.ID+"@"+v.Version)
		}
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// hasItem reports whether list contains want (exact element match).
func hasItem(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestPlatformFiltering pins the rule that makes platform-only languages safe to
// register: an entry declares the systems it exists on, and the registry views
// never offer one this host cannot run. Without this, Linux callers would see
// "powershell" and only discover it is Windows-only by failing.
func TestPlatformFiltering(t *testing.T) {
	all := All()
	for _, l := range all {
		if !l.On(runtime.GOOS) {
			t.Fatalf("All() offered %q, which does not exist on %s", l.ID, runtime.GOOS)
		}
	}
	// Anything this host cannot run must be unreachable through Get too, not just
	// absent from the list.
	for _, l := range registry {
		if l.On(runtime.GOOS) {
			continue
		}
		if _, ok := Get(l.ID); ok {
			t.Fatalf("Get(%q) resolved a language that does not exist on %s", l.ID, runtime.GOOS)
		}
	}
}

// TestWindowsOnlyEntriesAreRegistered guards against the filtering above being
// "fixed" by deleting the entries: they must still exist in the registry, just
// filtered per host. The two PowerShells are different products and must stay
// distinct, and nushell is the portable one.
func TestWindowsOnlyEntriesAreRegistered(t *testing.T) {
	byID := map[string]Lang{}
	for _, l := range registry {
		byID[l.ID] = l
	}
	if pw, ok := byID["powershell7"]; !ok {
		t.Fatal("powershell7 missing from the registry")
	} else if !pw.On("windows") || !pw.On("darwin") || !pw.On("linux") {
		t.Fatalf("powershell7 should be portable, GOOS=%v", pw.GOOS)
	}
	if ps, ok := byID["windowspowershell"]; !ok {
		t.Fatal("Windows PowerShell 5.1 missing from the registry")
	} else if ps.On("linux") {
		t.Fatal("Windows PowerShell must not claim to exist on linux")
	}
	if nu, ok := byID["nu"]; !ok {
		t.Fatal("nushell missing from the registry")
	} else if !nu.On("windows") || !nu.On("darwin") || !nu.On("linux") {
		t.Fatalf("nushell should be portable, GOOS=%v", nu.GOOS)
	}
}

// TestFileModeArgsSeparator: an interpreter whose driver has its own options
// needs "--" before the script's arguments, or `swift f.swift --help` shows
// swift's help instead of the script's. This is the difference between "the
// script got its arguments" and "the script silently got none".
func TestFileModeArgsSeparator(t *testing.T) {
	sw, ok := Get("swift")
	if !ok {
		t.Skipf("swift is not registered on %s", runtime.GOOS)
	}
	if !sw.ArgsNeedSep {
		t.Fatal("swift must set ArgsNeedSep")
	}
	argv, err := sw.Argv("/usr/bin/swift", "print(1)", "/tmp/x.swift",
		RunOpts{Args: []string{"--help", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	// Expect: swift /tmp/x.swift -- --help two
	want := []string{"/usr/bin/swift", "/tmp/x.swift", "--", "--help", "two"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", argv, want)
		}
	}

	// With no arguments there is nothing to separate, so no stray "--".
	argv, err = sw.Argv("/usr/bin/swift", "print(1)", "/tmp/x.swift", RunOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv {
		if a == "--" {
			t.Fatalf("argv = %v, want no separator when there are no args", argv)
		}
	}
}

// TestFileOnlyLanguagesGetAFileStub: a language with no inline-code flag can only
// be probed if the probe writes its stub to a FILE. Passing "" made Argv fail, so
// such a language could never be Found — which is a probe bug, not a language
// limitation.
func TestFileOnlyLanguagesGetAFileStub(t *testing.T) {
	for _, l := range All() {
		if l.ProgFlag != "" || l.Stub == "" {
			continue
		}
		// Argv with a path must succeed; with "" it must fail (that is exactly the
		// case the probe has to handle by materialising the file).
		if _, err := l.Argv("/bin/x", l.Stub, "/tmp/x"+stubSuffix(l), RunOpts{}); err != nil {
			t.Fatalf("%s: file-mode argv failed with a path: %v", l.ID, err)
		}
		if _, err := l.Argv("/bin/x", l.Stub, "", RunOpts{}); err == nil {
			t.Fatalf("%s: file-mode argv should require a path", l.ID)
		}
	}
}

// TestWindowsExtensionResolution: on Windows the probe stats a path to decide an
// interpreter exists, and an executable there has an extension. Statting the bare
// name found nothing, so a Windows host reported ZERO interpreters — caught the
// first time the windows-latest CI job ran, and invisible from Linux.
//
// The lookup is exercised through lookPath with a synthetic PATH, so this runs on
// any host: it asserts the CODE PATH rather than needing a Windows machine.
func TestWindowsExtensionResolution(t *testing.T) {
	dir := t.TempDir()
	// A file named like a Windows executable.
	exe := filepath.Join(dir, "fakeinterp.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewProbe([]string{"PATH=" + dir}, dir)

	// Bare-name resolution only appends extensions on Windows; on other platforms
	// the same name must NOT resolve, because there is no .exe there to run.
	got, err := p.lookPath([]string{"fakeinterp"})
	if runtime.GOOS == "windows" {
		if err != nil || got != exe {
			t.Fatalf("on windows, lookPath(fakeinterp) = %q, %v; want %q", got, err, exe)
		}
	} else if err == nil {
		t.Fatalf("on %s, lookPath(fakeinterp) should not resolve a .exe: got %q", runtime.GOOS, got)
	}
}
