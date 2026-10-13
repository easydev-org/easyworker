//go:build linux
package wayland

import (
	"encoding/binary"
	"syscall"
)

// unixRights builds the SCM_RIGHTS control message for sending file
// descriptors over a unix socket (standard library helper).
func unixRights(fds ...int) []byte {
	return syscall.UnixRights(fds...)
}

// parseRights extracts file descriptors from a received control message.
func parseRights(oob []byte) []int {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var out []int
	for _, m := range msgs {
		if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SCM_RIGHTS {
			for i := 0; i+4 <= len(m.Data); i += 4 {
				out = append(out, int(binary.LittleEndian.Uint32(m.Data[i:])))
			}
		}
	}
	return out
}
