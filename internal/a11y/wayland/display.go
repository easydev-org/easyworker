//go:build linux

package wayland

import (
	"encoding/binary"
	"fmt"
)

// wl_display interface (id 1). Requests: sync(0), get_registry(1).
const (
	wlDisplaySync        = 0
	wlDisplayGetRegistry = 1
)

// wl_registry requests: bind(0).
const wlRegistryBind = 0

// wl_callback events: done(0).
const wlCallbackDone = 0

// Sync sends wl_display.sync and returns the callback object id.
func (c *Conn) Sync() uint32 {
	cb := c.allocID("wl_callback")
	var e encoder
	e.u32(cb)
	if err := c.send(1, wlDisplaySync, &e); err != nil {
		return 0
	}
	return cb
}

// Roundtrip blocks until the server processes all pending requests.
func (c *Conn) Roundtrip() error {
	cb := c.Sync()
	if cb == 0 {
		return fmt.Errorf("sync failed")
	}
	for {
		ev, err := c.recvOne()
		if err != nil {
			return err
		}
		if ev.objectID == cb && ev.opcode == wlCallbackDone {
			return nil
		}
		c.dispatch(ev)
	}
}

// GetRegistry requests the wl_registry object.
func (c *Conn) GetRegistry() (uint32, error) {
	reg := c.allocID("wl_registry")
	c.registryID = reg
	var e encoder
	e.u32(reg)
	if err := c.send(1, wlDisplayGetRegistry, &e); err != nil {
		return 0, err
	}
	return reg, nil
}

// Bind binds a global to a new object of the given interface.
func (c *Conn) Bind(registryID, name, version uint32, iface string) (uint32, error) {
	id := c.allocID(iface)
	var e encoder
	e.u32(name)
	e.str(iface)
	e.u32(version)
	e.u32(id)
	if err := c.send(registryID, wlRegistryBind, &e); err != nil {
		return 0, err
	}
	return id, nil
}

// wl_registry events: global(0), global_remove(1).
func (c *Conn) handleRegistryEvent(ev *event) {
	switch ev.opcode {
	case 0: // global: name(u32), interface(str), version(u32)
		name, off := decU32(ev.args, 0)
		iface, off := decStr(ev.args, off)
		version, _ := decU32(ev.args, off)
		c.globals = append(c.globals, Global{Name: name, Interface: iface, Version: version})
	}
}

func (c *Conn) dispatch(ev *event) {
	switch c.objects[ev.objectID] {
	case "wl_registry":
		c.handleRegistryEvent(ev)
	}
}

// Globals returns the globals discovered so far (call after Roundtrip).
func (c *Conn) Globals() []Global { return c.globals }

// FindGlobal returns the first global of the given interface.
func (c *Conn) FindGlobal(iface string) (Global, bool) {
	for _, g := range c.globals {
		if g.Interface == iface {
			return g, true
		}
	}
	return Global{}, false
}

var _ = binary.LittleEndian
