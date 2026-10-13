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

// jobEnv inherits the worker's environment: arbitrary variables (e.g. a
// toolchain knob) must reach jobs without being enumerated anywhere.
func TestJobEnvInheritsArbitraryVars(t *testing.T) {
	t.Setenv("SOME_TOOLCHAIN_KNOB", "abc")
	t.Setenv("VCINSTALLDIR", `C:\VC`)

	env := jobEnv()
	if !envHas(env, "SOME_TOOLCHAIN_KNOB", "abc") {
		t.Errorf("inherited var missing: %v", env)
	}
	if !envHas(env, "VCINSTALLDIR", `C:\VC`) {
		t.Errorf("inherited var missing: %v", env)
	}
}

// A token that was unset before jobEnv (the normal startup order) must not
// appear in the job environment.
func TestJobEnvDropsUnsetToken(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "secret-token")
	_ = takeBootToken()

	if _, ok := envKey(jobEnv(), "WORKER_TOKEN"); ok {
		t.Fatal("jobEnv leaked WORKER_TOKEN")
	}
}

// PATH must be present even when the process has none (minimal container).
func TestJobEnvSynthesizesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only default")
	}
	// t.Setenv cannot unset; emulate a missing PATH by removing it via
	// os.Unsetenv and restoring afterwards.
	old, had := os.LookupEnv("PATH")
	_ = os.Unsetenv("PATH")
	defer func() {
		if had {
			_ = os.Setenv("PATH", old)
		}
	}()

	if _, ok := envKey(jobEnv(), "PATH"); !ok {
		t.Fatal("jobEnv did not synthesize PATH")
	}
}
