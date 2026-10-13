//go:build linux
package wayland

import "fmt"

// zwlr_virtual_pointer_manager_v1 requests:
//   create_virtual_pointer(seat, new_id)                = 0  (relative-only)
//   destroy()                                           = 1
//   create_virtual_pointer_with_output(seat, output,id) = 2  (absolute)
// The output-bound form is what makes MotionAbsolute land on the screen; the
// plain form's absolute requests are ignored. Sending 1 by mistake destroys the
// manager (broken pipe), so the opcodes matter.
const (
	vpManagerCreate        = 0
	vpManagerCreateWithOut = 2
)

// zwlr_virtual_pointer_v1 requests.
const (
	vpMotion          = 0 // time(u32), dx(fixed), dy(fixed)
	vpMotionAbsolute  = 1 // time, x, y, x_extent, y_extent (all u32)
	vpButton          = 2 // time, button, state
	vpAxis            = 3 // time, axis, value(fixed)
	vpFrame           = 4
	vpAxisSource      = 5
	vpAxisStop        = 6
	vpAxisDiscrete    = 7
	vpDestroy         = 8
)

// ButtonState
const (
	ButtonReleased = 0
	ButtonPressed  = 1
)

// Linux evdev button codes.
const (
	BtnLeft   = 0x110
	BtnRight  = 0x111
	BtnMiddle = 0x112
)

// Axis
const (
	AxisVertical   = 0
	AxisHorizontal = 1
)

// AxisSource
const (
	AxisSourceWheel = 0
)

// VirtualPointer wraps zwlr_virtual_pointer_v1.
type VirtualPointer struct {
	c      *Conn
	id     uint32
	seatID uint32
}

// CreateVirtualPointer binds the manager and creates a virtual pointer.
func (c *Conn) CreateVirtualPointer() (*VirtualPointer, error) {
	g, ok := c.FindGlobal("zwlr_virtual_pointer_manager_v1")
	if !ok {
		return nil, fmt.Errorf("compositor does not expose zwlr_virtual_pointer_manager_v1")
	}
	seat, ok := c.FindGlobal("wl_seat")
	if !ok {
		return nil, fmt.Errorf("no wl_seat")
	}
	mgr, err := c.Bind(c.registryID, g.Name, g.Version, "zwlr_virtual_pointer_manager_v1")
	if err != nil {
		return nil, err
	}
	seatID, err := c.Bind(c.registryID, seat.Name, 1, "wl_seat")
	if err != nil {
		return nil, err
	}
	// Bind an output (needed by create_virtual_pointer_with_output). Absent an
	// output, fall back to the relative-only create_virtual_pointer.
	var outputID uint32
	if outG, ok := c.FindGlobal("wl_output"); ok {
		if id, berr := c.Bind(c.registryID, outG.Name, outG.Version, "wl_output"); berr == nil {
			outputID = id
			// Drain the wl_output events (geometry/mode/scale/done) so the bind
			// completes before create_virtual_pointer_with_output references it.
			for i := 0; i < 8; i++ {
				_ = c.Roundtrip()
			}
		}
	}
	vp := c.allocID("zwlr_virtual_pointer_v1")
	var e encoder
	if outputID != 0 {
		e.u32(seatID)   // seat
		e.u32(outputID) // output
		e.u32(vp)       // new_id
		if err := c.send(mgr, vpManagerCreateWithOut, &e); err != nil {
			return nil, err
		}
	} else {
		e.u32(seatID) // seat
		e.u32(vp)     // new_id
		if err := c.send(mgr, vpManagerCreate, &e); err != nil {
			return nil, err
		}
	}
	return &VirtualPointer{c: c, id: vp, seatID: seatID}, nil
}

// Motion sends relative pointer motion.
func (vp *VirtualPointer) Motion(dx, dy float64) error {
	var e encoder
	e.u32(0)
	e.fixed(dx)
	e.fixed(dy)
	if err := vp.c.send(vp.id, vpMotion, &e); err != nil {
		return err
	}
	return vp.Frame()
}

// MotionAbsolute sends absolute pointer motion (x,y within 0..extent).
func (vp *VirtualPointer) MotionAbsolute(x, y, xExtent, yExtent uint32) error {
	var e encoder
	e.u32(0)
	e.u32(x)
	e.u32(y)
	e.u32(xExtent)
	e.u32(yExtent)
	if err := vp.c.send(vp.id, vpMotionAbsolute, &e); err != nil {
		return err
	}
	return vp.Frame()
}

// Button sends a button press/release (evdev code).
func (vp *VirtualPointer) Button(button uint32, pressed bool) error {
	state := uint32(ButtonReleased)
	if pressed {
		state = ButtonPressed
	}
	var e encoder
	e.u32(0)
	e.u32(button)
	e.u32(state)
	if err := vp.c.send(vp.id, vpButton, &e); err != nil {
		return err
	}
	return vp.Frame()
}

// Scroll sends a wheel axis event.
func (vp *VirtualPointer) Scroll(axis uint32, value float64) error {
	var e encoder
	e.u32(0)
	e.u32(axis)
	e.fixed(value)
	if err := vp.c.send(vp.id, vpAxisSource, &e); err != nil {
		return err
	}
	var es encoder
	es.u32(AxisSourceWheel)
	if err := vp.c.send(vp.id, vpAxisSource, &es); err != nil {
		return err
	}
	return vp.Frame()
}

// Frame ends an event group.
func (vp *VirtualPointer) Frame() error {
	var e encoder
	return vp.c.send(vp.id, vpFrame, &e)
}

// Close destroys the virtual pointer.
func (vp *VirtualPointer) Close() error {
	var e encoder
	return vp.c.send(vp.id, vpDestroy, &e)
}
