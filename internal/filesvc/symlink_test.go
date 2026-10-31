package filesvc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A workspace reached through a SYMLINK still reports its entries workspace-relative.
//
// THIS IS THE macOS FAILURE, reproduced anywhere: `t.TempDir()` under /var is /private/var on macOS, and
// any deployment whose workspace is a symlinked volume has the same shape. resolve() follows symlinks on
// the path it is handed; rel() compared that result against an UNRESOLVED root, the prefix never
// matched, and every entry came back absolute — so a client that re-roots a relative path put an
// absolute one under the workspace.
func TestRelThroughASymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows; the prefix logic is covered by the other tests")
	}
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "d", "f.bin"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	// A link whose target is the real directory: its spelling differs from what EvalSymlinks returns.
	link := filepath.Join(t.TempDir(), "ws-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	s := New(link)
	_, entries, _, err := s.List("d", 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0].Path; got != "d/f.bin" {
		t.Errorf("entry path = %q, want %q — an absolute path here is re-rooted by the client", got, "d/f.bin")
	}
}
