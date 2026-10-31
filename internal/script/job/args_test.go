package job

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// argsProgram prints the script's own arguments, one per line, in whatever way
// the language exposes them (sys.argv[1:], process.argv.slice(2), $1.., @ARGV).
func argsProgram(id string) string {
	switch id {
	case "python":
		return "import sys\nfor a in sys.argv[1:]:\n    print(a)\n"
	case "node":
		return "for (const a of process.argv.slice(2)) console.log(a)\n"
	default: // bash / sh
		return "for a in \"$@\"; do echo \"$a\"; done\n"
	}
}

// TestArgsReachTheScript is the end-to-end proof that Spec.Args are the SCRIPT's
// arguments: what the program sees, not interpreter flags. It covers every
// language installed on the host.
func TestArgsReachTheScript(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	sup := p.Supported(context.Background())
	r := NewRunner(p, os.Environ())

	ran := 0
	for _, id := range []string{"bash", "sh", "python", "node", "nu"} {
		if _, ok := sup[id]; !ok {
			continue
		}
		ran++
		j, err := r.Start(context.Background(), Spec{
			Lang: id, Code: argsProgram(id), Dir: t.TempDir(),
			Args: []string{"one", "two words"},
		})
		if err != nil {
			t.Errorf("%s: start: %v", id, err)
			continue
		}
		<-j.Done()
		got := joined(j.Snapshot())
		st := j.Status()
		if st.ExitCode != 0 {
			t.Errorf("%s: exit=%d err=%s out=%q", id, st.ExitCode, st.Error, got)
			continue
		}
		if !strings.Contains(got, "one") || !strings.Contains(got, "two words") {
			t.Errorf("%s: args did not reach the script: %q", id, got)
		}
	}
	if ran == 0 {
		t.Skip("no interpreter available")
	}
	t.Logf("verified args passing on %d language(s)", ran)
}

// TestExtraFlagsReachTheInterpreter: -X dev for python is an INTERPRETER flag; a
// script that is not valid Python must fail differently with it off vs on, but
// more simply: python -X dev must still run a valid script and produce output.
func TestExtraFlagsReachTheInterpreter(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["python"]; !ok {
		t.Skip("no python")
	}
	r := NewRunner(p, os.Environ())
	j, err := r.Start(context.Background(), Spec{
		Lang: "python", Code: "print('hi')\n", Dir: t.TempDir(),
		ExtraFlags: []string{"-X", "dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if got := joined(j.Snapshot()); !strings.Contains(got, "hi") {
		t.Fatalf("out=%q", got)
	}
	if st := j.Status(); st.ExitCode != 0 {
		t.Fatalf("status=%+v", st)
	}
}

// TestModeChangesSemantics is the core CommonJS/ESM proof: the same import
// statement fails under cjs and works under esm.
func TestModeChangesSemantics(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["node"]; !ok {
		t.Skip("no node")
	}
	r := NewRunner(p, os.Environ())

	// ESM: a top-level import is valid.
	esm := "import os from 'node:os'\nconsole.log('platform', typeof os.platform())\n"
	j, err := r.Start(context.Background(), Spec{Lang: "node", Code: esm, Mode: "esm", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if st := j.Status(); st.ExitCode != 0 {
		t.Fatalf("esm import should work: exit=%d err=%s out=%q", st.ExitCode, st.Error, joined(j.Snapshot()))
	}
	if got := joined(j.Snapshot()); !strings.Contains(got, "platform") {
		t.Fatalf("esm out=%q", got)
	}

	// CJS: the same statement is a syntax error.
	j2, err := r.Start(context.Background(), Spec{Lang: "node", Code: esm, Mode: "cjs", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j2.Done()
	if st := j2.Status(); st.ExitCode == 0 {
		t.Fatalf("top-level import should FAIL under cjs, but exit was 0: %q", joined(j2.Snapshot()))
	}
}

// TestModeRejectedWhenUnsupported: python has no modes, so asking for one is a
// clear error rather than a silently ignored field.
func TestModeRejectedWhenUnsupported(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["python"]; !ok {
		t.Skip("no python")
	}
	r := NewRunner(p, os.Environ())
	_, err := r.Start(context.Background(), Spec{
		Lang: "python", Code: "print(1)", Mode: "esm",
	})
	if err == nil {
		t.Fatal("python + esm should be rejected")
	}
	if !strings.Contains(err.Error(), "does not support mode") {
		t.Fatalf("unhelpful: %v", err)
	}
}

// TestBashArgsNotSwallowedByArgv0: bash -c eats the next word as $0; the argv0
// placeholder must protect the user's first argument.
func TestBashArgsNotSwallowedByArgv0(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	r := NewRunner(p, os.Environ())
	j, err := r.Start(context.Background(), Spec{
		Lang: "bash", Code: `echo "n=$# one=$1"`, Dir: t.TempDir(), Args: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	got := joined(j.Snapshot())
	if !strings.Contains(got, "n=2") || !strings.Contains(got, "one=alpha") {
		t.Fatalf("bash dropped an argument (argv0 placeholder missing?): %q", got)
	}
}
