package script

import (
	"context"
	"runtime"
	"testing"
)

// TestDefaultLanguageIsRunnable: the worker's default must be a language this
// host can ACTUALLY run. A hardcoded "bash" was wrong on Windows, where bash only
// exists if Git for Windows or WSL happens to be installed, while PowerShell ships
// with the OS — every command on a plain Windows image would have failed to start.
func TestDefaultLanguageIsRunnable(t *testing.T) {
	r := New(t.TempDir())
	def := r.Language()
	if def == "" {
		t.Fatal("no default language")
	}
	sup := r.Engine().Supported(context.Background())
	if len(sup) == 0 {
		t.Skipf("no interpreter on this host; default is %q", def)
	}
	if _, ok := sup[def]; !ok {
		ids := make([]string, 0, len(sup))
		for id := range sup {
			ids = append(ids, id)
		}
		t.Fatalf("default %q is not runnable here (available: %v)", def, ids)
	}
}

// TestDefaultRunsAProgram is the end-to-end version: use the default and check
// that a command actually produces output. A default that probes as available but
// cannot run a program is worse than none.
func TestDefaultRunsAProgram(t *testing.T) {
	r := New(t.TempDir())
	if len(r.Engine().Supported(context.Background())) == 0 {
		t.Skip("no interpreter on this host")
	}
	code, want := defaultProgram(r.Language())
	if code == "" {
		t.Skipf("no sample program for %q", r.Language())
	}
	var out capture
	res, err := r.Run(context.Background(), Spec{Code: code, Stdout: &out})
	if err != nil {
		t.Fatalf("%s: %v", r.Language(), err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("%s exited %d: %q", r.Language(), res.ExitCode, out.s)
	}
	if !contains(out.s, want) {
		t.Fatalf("%s output = %q, want %q", r.Language(), out.s, want)
	}
}

// TestPlatformCandidatesCoverEveryPlatform guards the table itself: a GOOS the
// worker might run on must have candidates, or the lookup silently returns only
// the fallback.
func TestPlatformCandidatesCoverEveryPlatform(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if len(defaultCandidates[goos]) == 0 {
			t.Fatalf("no default candidates for %s", goos)
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Logf("note: no candidate list for %s; the fallback will be used", runtime.GOOS)
	}
}

type capture struct{ s string }

func (c *capture) Write(p []byte) (int, error) { c.s += string(p); return len(p), nil }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func defaultProgram(lang string) (string, string) {
	switch lang {
	case "bash", "sh", "zsh":
		return `echo easyscript-default`, "easyscript-default"
	case "python":
		return `print("easyscript-default")`, "easyscript-default"
	case "node":
		return `console.log("easyscript-default")`, "easyscript-default"
	case "powershell7", "windowspowershell":
		return `Write-Output 'easyscript-default'`, "easyscript-default"
	case "nu":
		return `print easyscript-default`, "easyscript-default"
	case "swift":
		return `print("easyscript-default")`, "easyscript-default"
	}
	return "", ""
}
