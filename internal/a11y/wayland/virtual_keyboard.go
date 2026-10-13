//go:build linux
package wayland

import (
	"fmt"
	"os"
	"syscall"
)

// zwp_virtual_keyboard_manager_v1: create_virtual_keyboard(seat, new_id)=0
const vkManagerCreate = 0

// zwp_virtual_keyboard_v1 requests.
const (
	vkKeymap    = 0 // format(u32), fd, size(u32)
	vkKey       = 1 // time(u32), key(u32), state(u32)
	vkModifiers = 2 // depressed, latched, locked, group
	vkDestroy   = 3
)

// KeymapFormat
const (
	KeymapFormatXkbV1 = 1
)

// KeyState
const (
	KeyReleased = 0
	KeyPressed  = 1
)

// VirtualKeyboard wraps zwp_virtual_keyboard_v1.
type VirtualKeyboard struct {
	c      *Conn
	id     uint32
	seatID uint32
}

// CreateVirtualKeyboard binds the manager and creates a virtual keyboard.
func (c *Conn) CreateVirtualKeyboard() (*VirtualKeyboard, error) {
	g, ok := c.FindGlobal("zwp_virtual_keyboard_manager_v1")
	if !ok {
		return nil, fmt.Errorf("compositor does not expose zwp_virtual_keyboard_manager_v1")
	}
	seat, ok := c.FindGlobal("wl_seat")
	if !ok {
		return nil, fmt.Errorf("no wl_seat")
	}
	mgr, err := c.Bind(c.registryID, g.Name, g.Version, "zwp_virtual_keyboard_manager_v1")
	if err != nil {
		return nil, err
	}
	seatID, err := c.Bind(c.registryID, seat.Name, 1, "wl_seat")
	if err != nil {
		return nil, err
	}
	vk := c.allocID("zwp_virtual_keyboard_v1")
	var e encoder
	e.u32(seatID) // seat
	e.u32(vk)     // new_id
	if err := c.send(mgr, vkManagerCreate, &e); err != nil {
		return nil, err
	}
	return &VirtualKeyboard{c: c, id: vk, seatID: seatID}, nil
}

// SetKeymap uploads an xkb keymap via a memfd.
func (vk *VirtualKeyboard) SetKeymap(xkb string) error {
	fd, err := memfdCreate("xkb-keymap")
	if err != nil {
		return fmt.Errorf("memfd: %w", err)
	}
	fmt.Fprintf(os.Stderr, "DEBUG keymap fd=%d len=%d\n", fd, len(xkb))
	if _, err := syscall.Write(fd, []byte(xkb)); err != nil {
		syscall.Close(fd)
		return err
	}
	var e encoder
	e.u32(KeymapFormatXkbV1)
	e.fd(fd) // placeholder; actual fd sent via SCM_RIGHTS
	e.u32(uint32(len(xkb) + 1))
	if err := vk.c.send(vk.id, vkKeymap, &e); err != nil {
		syscall.Close(fd)
		return err
	}
	syscall.Close(fd)
	return nil
}

// Key sends a key press/release (evdev keycode, +8 offset per protocol).
func (vk *VirtualKeyboard) Key(keycode uint32, pressed bool) error {
	state := uint32(KeyReleased)
	if pressed {
		state = KeyPressed
	}
	var e encoder
	e.u32(0) // time
	e.u32(keycode)
	e.u32(state)
	return vk.c.send(vk.id, vkKey, &e)
}

// Modifiers sets the modifier state.
func (vk *VirtualKeyboard) Modifiers(depressed, latched, locked, group uint32) error {
	var e encoder
	e.u32(depressed)
	e.u32(latched)
	e.u32(locked)
	e.u32(group)
	return vk.c.send(vk.id, vkModifiers, &e)
}

// Close destroys the virtual keyboard.
func (vk *VirtualKeyboard) Close() error {
	var e encoder
	return vk.c.send(vk.id, vkDestroy, &e)
}

// memfdCreate creates an anonymous in-memory file (Linux memfd).
func memfdCreate(name string) (int, error) {
	fd, err := syscallMemfdCreate(name)
	if err != nil {
		// Fall back to a temp file.
		f, ferr := os.CreateTemp("", "goa11y-keymap-*")
		if ferr != nil {
			return -1, err
		}
		defer os.Remove(f.Name())
		return int(f.Fd()), nil
	}
	return fd, nil
}
