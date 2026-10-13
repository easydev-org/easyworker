package shellh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddPathsReachesJobs verifies a directory added at runtime is resolvable
// by a SUBSEQUENT job (the on-demand toolchain path-visibility contract).
func TestAddPathsReachesJobs(t *testing.T) {
	ws := t.TempDir()
	r := New(ws, []string{"PATH=/usr/bin:/bin"})

	toolDir := filepath.Join(ws, "opt", "tool")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "mytool"), []byte("#!/bin/sh\necho hello-tool\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Before AddPaths the tool is not on PATH.
	var out strings.Builder
	if res, err := r.Run(context.Background(), "mytool", "", nil, &out, &out); err != nil || res.ExitCode == 0 {
		t.Fatalf("tool unexpectedly resolvable before AddPaths: exit=%d err=%v", res.ExitCode, err)
	}

	r.AddPaths(toolDir)
	out.Reset()
	res, err := r.Run(context.Background(), "mytool", "", nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(out.String(), "hello-tool") {
		t.Fatalf("tool not resolvable after AddPaths: exit=%d out=%q", res.ExitCode, out.String())
	}

	// Idempotent: adding again must not duplicate the entry.
	r.AddPaths(toolDir)
	count := 0
	for _, p := range strings.Split(strings.TrimPrefix(pathEnv(r.Env), "PATH="), string(os.PathListSeparator)) {
		if p == toolDir {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("toolDir appears %d times, want 1", count)
	}
}

func pathEnv(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return kv
		}
	}
	return ""
}
