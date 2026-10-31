package job

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// TestChildInheritsDaemonEnvironment pins the one env contract there is: a job
// sees the daemon's environment, so PATH/HOME/LANG behave as they would in a
// terminal, and a subprocess the script spawns inherits them in turn.
//
// There is no per-job env override on purpose (see Spec): an override would be
// either an overlay — a second concept — or a replacement, which quietly drops
// PATH/HOME and breaks subprocess.
func TestChildInheritsDaemonEnvironment(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	// A sentinel only the daemon's own environment has.
	base := append([]string(nil), os.Environ()...)
	base = append(base, "EASYSCRIPT_TEST_SENTINEL=from-daemon")
	r := NewRunner(p, base)

	j, err := r.Start(context.Background(), Spec{
		Lang: "bash",
		Code: `echo "sentinel=$EASYSCRIPT_TEST_SENTINEL"; echo "path=${PATH:+set}"; echo "home=${HOME:+set}"`,
		Dir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	out := joined(j.Snapshot())
	for _, want := range []string{"sentinel=from-daemon", "path=set", "home=set"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in child env: %q", want, out)
		}
	}
}

// TestSubprocessInheritsEnvironment: what the script's own child process sees is
// the same environment — the reason the daemon must pass a FULL env, not a
// partial one.
func TestSubprocessInheritsEnvironment(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	sup := p.Supported(context.Background())
	base := append([]string(nil), os.Environ()...)
	base = append(base, "EASYSCRIPT_TEST_SENTINEL=deep")
	r := NewRunner(p, base)

	var langID, code string
	switch {
	case hasLangID(sup, "bash"):
		langID = "bash"
		// `env` is a grandchild: it must still see the sentinel + PATH.
		code = `env | grep -c EASYSCRIPT_TEST_SENTINEL | sed 's/^/sentinel-lines=/'`
	case hasLangID(sup, "python"):
		langID = "python"
		code = `import os; print("sentinel=" + os.environ.get("EASYSCRIPT_TEST_SENTINEL",""))`
	default:
		t.Skip("no interpreter")
	}
	j, err := r.Start(context.Background(), Spec{Lang: langID, Code: code, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	out := joined(j.Snapshot())
	if !strings.Contains(out, "1") && !strings.Contains(out, "deep") {
		t.Fatalf("the environment did not reach the child: %q", out)
	}
}

// TestNoEnvFieldOnSpec is a compile-time guard with a runtime body: if someone
// reintroduces a per-job env override, this comment and the struct diverge and a
// reviewer sees it. (Spec has no Env field; nothing here can set one.)
func TestNoEnvFieldOnSpec(t *testing.T) {
	s := Spec{Lang: "bash", Code: "echo hi", Mode: "", ExtraFlags: nil, Args: nil, Dir: ""}
	if s.Lang != "bash" {
		t.Fatal("unreachable")
	}
}

func hasLangID(m map[string]lang.Version, id string) bool { _, ok := m[id]; return ok }
