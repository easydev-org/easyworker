package toolchains

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPlatformArtifactFilter: only artifacts matching the current OS/Arch (or
// with empty OS/Arch) are selected — ONE index serves linux AND windows/macos.
func TestPlatformArtifactFilter(t *testing.T) {
	all := []Artifact{
		{URL: "u-linux", OS: "linux", Arch: "amd64"},
		{URL: "u-win", OS: "windows", Arch: "amd64"},
		{URL: "u-mac", OS: "darwin", Arch: "arm64"},
		{URL: "u-any"}, // wildcard
	}
	got := map[string]bool{}
	for _, a := range platformArtifacts(all, "windows", "amd64") {
		got[a.URL] = true
	}
	if !got["u-win"] || !got["u-any"] || got["u-linux"] || got["u-mac"] {
		t.Fatalf("windows/amd64 selection = %v", got)
	}
}

// TestToolchainOSGate: a toolchain restricted to some OS is REJECTED on another
// (the index can say "flutter is macOS/Windows only", so a linux sandbox
// refuses it instead of installing something useless).
func TestToolchainOSGate(t *testing.T) {
	if !(Toolchain{}).allowsOS("linux") {
		t.Fatal("empty OS must allow any platform")
	}
	tc := Toolchain{OS: []string{"windows", "darwin"}}
	if tc.allowsOS("linux") {
		t.Fatal("linux must be refused")
	}
	if !tc.allowsOS("darwin") {
		t.Fatal("darwin must be allowed")
	}
}

// TestEnsureRejectsWrongOS: Ensure fails clearly when the toolchain is not
// installable on this platform.
func TestEnsureRejectsWrongOS(t *testing.T) {
	idx := `{"schema":1,"toolchains":{
      "flutter":{"os":["windows","darwin"],"versions":{"1.0":{"artifacts":[{"url":"u","sha256":"x","format":"raw"}]}}}
    }}`
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")
	if err := os.WriteFile(idxPath, []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	in := New(filepath.Join(dir, "root"), idxPath, nil, nil)
	_, err := in.Ensure(context.Background(), []Spec{{Name: "flutter", Version: "1.0"}})
	if err == nil {
		t.Fatal("expected an error on a platform flutter is not allowed on")
	}
	if runtime.GOOS == "linux" && !strings.Contains(err.Error(), "not installable on linux") {
		t.Fatalf("error = %v", err)
	}
}
