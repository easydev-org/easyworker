//go:build linux
// Package wayland implements a minimal, dependency-free Wayland client in pure
// Go. It speaks the wire protocol directly (unix socket + little-endian binary
// messages) so no libwayland, cgo, or external tools (wtype/wlrctl) are needed.
//
// Only the subset required for input injection is implemented: wl_display,
// wl_registry, wl_callback, wl_seat, plus the virtual-keyboard and
// virtual-pointer protocols.
package wayland

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
)

// Conn is a Wayland connection.
type Conn struct {
	conn    *net.UnixConn
	nextID  uint32
	objects map[uint32]string // id → interface name (for debugging)
	globals []Global
	registryID uint32
	rbuf    []byte
	rfds    []int
}

// Global is an advertised wl_registry global.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Dial connects to the Wayland display named by WAYLAND_DISPLAY (default
// wayland-0) under XDG_RUNTIME_DIR.
func Dial() (*Conn, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp/xdg"
	}
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		name = "wayland-0"
	}
	path := name
	if !filepath.IsAbs(name) {
		path = filepath.Join(dir, name)
	}
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	c, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("connect wayland socket %s: %w", path, err)
	}
	wc := &Conn{conn: c, nextID: 2, objects: map[uint32]string{1: "wl_display"}}
	return wc, nil
}

// Close closes the connection.
func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) allocID(iface string) uint32 {
	id := c.nextID
	c.nextID++
	c.objects[id] = iface
	return id
}

// ── message encoding ───────────────────────────────────────────────

type encoder struct {
	buf []byte
	fds []int
}

func (e *encoder) u32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }
func (e *encoder) i32(v int32)  { e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(v)) }
func (e *encoder) fixed(v float64) { e.u32(uint32(int32(v * 256))) }

func (e *encoder) str(s string) {
	if s == "" {
		e.u32(0)
		return
	}
	e.u32(uint32(len(s) + 1))
	e.buf = append(e.buf, []byte(s)...)
	e.buf = append(e.buf, 0)
	e.pad()
}

// fd records a file descriptor to be sent as SCM_RIGHTS ancillary data. In the
// Wayland wire format an fd argument occupies ZERO bytes in the message body.
func (e *encoder) fd(f int) {
	e.fds = append(e.fds, f)
}

func (e *encoder) pad() {
	for len(e.buf)%4 != 0 {
		e.buf = append(e.buf, 0)
	}
}

// send writes one request: objectID, opcode, and encoded args.
func (c *Conn) send(objectID, opcode uint32, e *encoder) error {
	e.pad()
	size := 8 + len(e.buf)
	msg := make([]byte, 8, size)
	binary.LittleEndian.PutUint32(msg[0:4], objectID)
	binary.LittleEndian.PutUint32(msg[4:8], uint32(size)<<16|opcode)
	msg = append(msg, e.buf...)

	var oob []byte
	if len(e.fds) > 0 {
		oob = unixRights(e.fds...)
	}
	if os.Getenv("GOA11Y_DEBUG_MSG") != "" {
		fmt.Fprintf(os.Stderr, "SEND obj=%d op=%d size=%d bytes=% x\n", objectID, opcode, size, msg)
	}
	_, _, err := c.conn.WriteMsgUnix(msg, oob, nil)
	return err
}

// ── message decoding ───────────────────────────────────────────────

type event struct {
	objectID uint32
	opcode   uint16
	args     []byte
	fds      []int
}

// recvOne reads one event, buffering any extra messages a single read returns.
// A unix-socket read may deliver several Wayland messages at once, so leftover
// bytes are kept and replayed.
func (c *Conn) recvOne() (*event, error) {
	for {
		if len(c.rbuf) >= 8 {
			word := binary.LittleEndian.Uint32(c.rbuf[4:8])
			size := int(word >> 16)
			if size < 8 || size > len(c.rbuf) {
				return nil, fmt.Errorf("bad wayland message size %d (have %d)", size, len(c.rbuf))
			}
			objectID := binary.LittleEndian.Uint32(c.rbuf[0:4])
			opcode := uint16(word & 0xffff)
			args := append([]byte(nil), c.rbuf[8:size]...)
			c.rbuf = c.rbuf[size:]
			return &event{objectID: objectID, opcode: opcode, args: args}, nil
		}
		buf := make([]byte, 8192)
		oob := make([]byte, 256)
		n, oobn, _, _, err := c.conn.ReadMsgUnix(buf, oob)
		if err != nil {
			return nil, err
		}
		c.rbuf = append(c.rbuf, buf[:n]...)
		if oobn > 0 {
			c.rfds = append(c.rfds, parseRights(oob[:oobn])...)
		}
	}
}

// decode helpers
func decU32(b []byte, off int) (uint32, int) { return binary.LittleEndian.Uint32(b[off:]), off + 4 }
func decStr(b []byte, off int) (string, int) {
	n := binary.LittleEndian.Uint32(b[off:])
	off += 4
	if n == 0 {
		return "", off
	}
	s := string(b[off : off+int(n)-1])
	off += int(n)
	// pad
	for off%4 != 0 {
		off++
	}
	return s, off
}

func fixedToFloat(v uint32) float64 { return float64(int32(v)) / 256.0 }

var _ = math.Round
