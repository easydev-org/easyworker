package job

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// expectedHere lists the languages a host is EXPECTED to provide out of the box,
// keyed by GOOS. It is the assertion CI runs on each platform: registering a
// Windows-only or macOS-only language is only worth anything if the entry
// actually runs a program there.
//
// A language in this map that the probe cannot find FAILS the test: on the
// platform that owns it, "not found" means the registry entry is wrong (bad
// candidate name, bad flags), not that the image is thin. Languages NOT in the
// list are skipped when absent, because they are optional installs.
var expectedHere = map[string][]string{
	// Windows: BOTH PowerShells. 5.1 ships with the OS; 7 is installed by the CI
	// job. Testing only the one that happens to be preinstalled would leave the
	// cross-platform entry — the one most callers will actually use — unverified
	// on the platform where its flags matter most.
	"windows": {"windowspowershell", "powershell7"},
	"darwin":  {"zsh"}, // the default macOS shell
	"linux":   {"sh"},  // present in every image
}

// TestPlatformLanguageRuns is the per-platform proof: for each language this OS
// is expected to own, run a real program through the engine and check the output.
//
// This is deliberately an EXECUTION test, not a lookup test. `command -v pwsh`
// succeeding proves nothing about ProgFlag, Argv0 or ArgsNeedSep — the parts that
// actually break. Running `Write-Output` and reading it back does.
func TestPlatformLanguageRuns(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	sup := p.Supported(context.Background())

	for _, id := range expectedHere[runtime.GOOS] {
		if _, ok := lang.Get(id); !ok {
			t.Fatalf("%s: %q is not registered on this host", runtime.GOOS, id)
		}
		if _, found := sup[id]; !found {
			// Report what IS there: a bare "not found" sends the reader hunting.
			ids := make([]string, 0, len(sup))
			for k := range sup {
				ids = append(ids, k)
			}
			t.Fatalf("%s: %q was expected on this host but the probe did not find it (found: %v)",
				runtime.GOOS, id, ids)
		}

		code, want := helloProgram(id)
		if code == "" {
			t.Fatalf("%s: no hello program for %q — add one so this test proves something", runtime.GOOS, id)
		}
		r := NewRunner(p, os.Environ())
		j, err := r.Start(context.Background(), Spec{
			Lang: id, Code: code, Dir: t.TempDir(),
			// The programs below read their first argument, which is also what
			// exercises the invocation shape: a misplaced -Command or a missing
			// "--" separator drops the argument silently.
			Args: []string{"from-test"},
		})
		if err != nil {
			t.Fatalf("%s: start %s: %v", runtime.GOOS, id, err)
		}
		select {
		case <-j.Done():
		case <-time.After(60 * time.Second):
			t.Fatalf("%s: %s did not finish", runtime.GOOS, id)
		}
		var out strings.Builder
		for _, e := range j.Snapshot() {
			out.WriteString(e.Data)
		}
		if st := j.Status(); st.ExitCode != 0 {
			t.Fatalf("%s: %s exited %d (%s): output=%q", runtime.GOOS, id, st.ExitCode, st.Error, out.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%s: %s output = %q, want it to contain %q", runtime.GOOS, id, out.String(), want)
		}
	}
}

// helloProgram returns (program, expected substring) for a language: something
// small that proves the interpreter ran THIS text, plus argument passing where the
// language's flags are easy to get wrong.
func helloProgram(id string) (string, string) {
	switch id {
	case "windowspowershell", "powershell7":
		// Index the argument INSIDE the expression: "hello-$($args[0])" was parsed
		// as the literal "hello-" followed by $($args[0]) in a separate statement,
		// so the output was "hello-\r\nfrom-test" — the test caught it.
		return `Write-Output ("hello-" + $args[0])`, "hello-from-test"
	case "zsh", "bash", "sh":
		return `echo "hello-$1"`, "hello-from-test"
	case "swift":
		return `print("hello-" + CommandLine.arguments[1])`, "hello-from-test"
	case "nu":
		return `print $"hello-($in)"`, "hello-"
	}
	return "", ""
}
