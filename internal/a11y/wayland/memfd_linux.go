//go:build linux
package wayland

import "golang.org/x/sys/unix"

// syscallMemfdCreate creates an anonymous memory file (Linux memfd_create).
func syscallMemfdCreate(name string) (int, error) {
	return unix.MemfdCreate(name, 0)
}
