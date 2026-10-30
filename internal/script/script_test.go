package script

import (
	"context"
	"errors"
	"testing"
)

// TestNoLanguageIsRejected is the contract this package now has: a run with no
// language FAILS. It used to fall back to a "default", which could not be right on
// two platforms at once (the same text is valid bash and invalid Windows
// PowerShell 5.1), so a command could silently run as a different language from
// the one it was written in and still exit 0.
func TestNoLanguageIsRejected(t *testing.T) {
	r := New(t.TempDir())
	_, err := r.Run(context.Background(), Spec{Code: "echo hi"})
	if err == nil {
		t.Fatal("a run with no language must fail")
	}
	if !errors.Is(err, ErrNoLanguage) {
		t.Fatalf("err = %v, want ErrNoLanguage", err)
	}
	// A whitespace-only language is the same mistake.
	if _, err := r.Run(context.Background(), Spec{Code: "echo hi", Lang: "   "}); !errors.Is(err, ErrNoLanguage) {
		t.Fatalf("blank lang: err = %v, want ErrNoLanguage", err)
	}
}

// TestNoLanguageErrorNamesTheOptions: the error must be actionable. Telling a
// caller "lang required" while withholding what IS installed makes them guess at
// ids.
func TestNoLanguageErrorNamesTheOptions(t *testing.T) {
	r := New(t.TempDir())
	langs := r.Languages(context.Background())
	_, err := r.Run(context.Background(), Spec{Code: "echo hi"})
	if err == nil {
		t.Fatal("want an error")
	}
	if len(langs) == 0 {
		if !contains(err.Error(), "no interpreter") {
			t.Fatalf("on a host with nothing installed the error should say so: %v", err)
		}
		return
	}
	for _, l := range langs {
		if !contains(err.Error(), l.ID) {
			t.Fatalf("error does not mention available id %q: %v", l.ID, err)
		}
	}
}

// TestExplicitLanguageRuns: naming a language works, and the result reports it.
func TestExplicitLanguageRuns(t *testing.T) {
	r := New(t.TempDir())
	for _, l := range r.Languages(context.Background()) {
		code, want := defaultProgram(l.ID)
		if code == "" {
			continue
		}
		var out capture
		res, err := r.Run(context.Background(), Spec{Lang: l.ID, Code: code, Stdout: &out})
		if err != nil {
			t.Fatalf("%s: %v", l.ID, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("%s exited %d: %q", l.ID, res.ExitCode, out.s)
		}
		if res.Lang != l.ID {
			t.Fatalf("%s: Result.Lang = %q", l.ID, res.Lang)
		}
		if !contains(out.s, want) {
			t.Fatalf("%s output = %q, want %q", l.ID, out.s, want)
		}
		return // one interpreter is enough to prove the path
	}
	t.Skip("no interpreter with a sample program on this host")
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
