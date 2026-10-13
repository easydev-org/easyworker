//go:build windows

package toolchains

import "os"

// lockRoot on Windows is best-effort: the toolchain installer targets linux
// sandboxes, so we only ensure the root exists (no cross-process lock).
func lockRoot(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return func() {}, nil
}
