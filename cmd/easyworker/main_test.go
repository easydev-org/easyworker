package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func envHas(env []string, key, val string) bool {
	want := key + "=" + val
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func envKey(env []string, key string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return kv[len(key)+1:], true
		}
	}
	return "", false
}

// takeBootToken must return the token AND remove it from the process
// environment so jobs (which inherit that environment) cannot see it.
func TestTakeBootTokenRemovesFromEnv(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "secret-token")

	got := takeBootToken()
	if got != "secret-token" {
		t.Fatalf("takeBootToken = %q want secret-token", got)
	}
	if _, ok := os.LookupEnv("WORKER_TOKEN"); ok {
		t.Fatal("WORKER_TOKEN still present in the process environment")
	}
}

// Jobs inherit the worker's process environment, so an arbitrary variable (a
// toolchain knob, say) must reach them without being enumerated anywhere.
// ensureProcessEnv only ADDS missing keys, so this must survive untouched.
func TestProcessEnvKeepsArbitraryVars(t *testing.T) {
	t.Setenv("SOME_TOOLCHAIN_KNOB", "abc")
	t.Setenv("VCINSTALLDIR", `C:\VC`)

	ensureProcessEnv()

	if got := os.Getenv("SOME_TOOLCHAIN_KNOB"); got != "abc" {
		t.Errorf("SOME_TOOLCHAIN_KNOB = %q, want abc", got)
	}
	if got := os.Getenv("VCINSTALLDIR"); got != `C:\VC` {
		t.Errorf("VCINSTALLDIR = %q", got)
	}
}

// A token that was unset before startup (the normal order) must not be in the
// environment jobs inherit.
func TestProcessEnvDropsUnsetToken(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "secret-token")
	_ = takeBootToken()

	if _, ok := os.LookupEnv("WORKER_TOKEN"); ok {
		t.Fatal("WORKER_TOKEN survived takeBootToken")
	}
}

// PATH must be present even when the process has none (a minimal container),
// because a job cannot resolve its own interpreter without it.
func TestProcessEnvSynthesizesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only default")
	}
	old, had := os.LookupEnv("PATH")
	_ = os.Unsetenv("PATH")
	defer func() {
		_ = os.Unsetenv("PATH")
		if had {
			_ = os.Setenv("PATH", old)
		}
	}()

	ensureProcessEnv()

	if _, ok := os.LookupEnv("PATH"); !ok {
		t.Fatal("ensureProcessEnv did not synthesize PATH")
	}
}
