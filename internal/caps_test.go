package internal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// resetGitProbe clears the once-cache so a test can control PATH.
func resetGitProbe(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test is unix-only")
	}
	gitMu.Lock()
	gitDone, gitOK, gitVersion = false, false, ""
	gitMu.Unlock()
}

// TestGitProbeAbsent: no git on PATH -> false, "".
func TestGitProbeAbsent(t *testing.T) {
	resetGitProbe(t)
	t.Setenv("PATH", t.TempDir()) // empty dir: nothing to find
	ok, v := gitProbe()
	if ok || v != "" {
		t.Fatalf("got (%v, %q), want (false, \"\")", ok, v)
	}
}

// TestGitProbeUnrunnable is the case that matters for Android: a `git` on PATH
// that cannot execute (wrong libc / no loader) must NOT be reported as usable.
func TestGitProbeUnrunnable(t *testing.T) {
	resetGitProbe(t)
	dir := t.TempDir()
	// An executable file that is not a valid program.
	p := filepath.Join(dir, "git")
	if err := os.WriteFile(p, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ok, v := gitProbe()
	if ok || v != "" {
		t.Fatalf("a broken git must report unavailable; got (%v, %q)", ok, v)
	}
}

// TestGitProbeReal: when the host has a working git, report it and its version.
func TestGitProbeReal(t *testing.T) {
	resetGitProbe(t)
	// Keep the real PATH so the test host's git is found.
	ok, v := gitProbe()
	if !ok {
		t.Skip("no git on this host")
	}
	if v == "" {
		t.Fatal("git available but version is empty")
	}
}
