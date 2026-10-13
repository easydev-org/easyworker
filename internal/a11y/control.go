package goa11y

import (
	"encoding/binary"
	"strings"
	"time"
)

// Remote-input protocol carried over the WebRTC "control" DataChannel.
//
// The browser sends compact binary messages that map 1:1 onto the platform's
// InputProvider primitives, so a viewer can drive the sandbox exactly like the
// noVNC view does (pointer, wheel, keys, text) plus a two-way text clipboard.
// Every message is [type:u8][payload...]; coordinates are in the SOURCE
// (captured) pixel space — the browser scales from its <video> box.
const (
	ctrlMouseMove = 0x01 // x:i32 y:i32
	ctrlMouseDown = 0x02 // x:i32 y:i32 btn:u8
	ctrlMouseUp   = 0x03 // btn:u8
	ctrlWheel     = 0x04 // x:i32 y:i32 dx:i32 dy:i32
	ctrlKeyDown   = 0x05 // nameLen:u8 name char:u32 mods:u8
	ctrlKeyUp     = 0x06 // nameLen:u8 name char:u32 mods:u8
	ctrlText      = 0x07 // utf8 bytes (typed text)
	ctrlHello     = 0x08 // srcW:i32 srcH:i32 (browser reports its viewport)
	ctrlClipSet   = 0x10 // utf8 (browser -> sandbox clipboard)
	ctrlClipGet   = 0x11 // (browser asks for the sandbox clipboard)
	ctrlClipData  = 0x12 // utf8 (sandbox -> browser clipboard)
	ctrlSystemKey = 0x20 // nameLen:u8 name  (device/system key; platform-mapped)
	ctrlGesture   = 0x21 // kind:u8 x:i32 y:i32 x2:i32 y2:i32 ms:u32
	// Touch events (finger): distinct from the mouse so a device can route them
	// to its native touchscreen (Android) while the mouse drives a real cursor.
	ctrlTouchDown = 0x22 // x:i32 y:i32
	ctrlTouchMove = 0x23 // x:i32 y:i32
	ctrlTouchUp   = 0x24 // (no body)
)

// SystemKey names (device/OS-level keys with no plain keyboard equivalent).
// The platform maps each to its native action; unsupported names are ignored.
const (
	SysHome          = "home"
	SysBack          = "back"
	SysRecents       = "recents"
	SysMenu          = "menu"
	SysNotifications = "notifications"
	SysVolumeUp      = "volume_up"
	SysVolumeDown    = "volume_down"
	SysVolumeMute    = "volume_mute"
	SysPower         = "power"
)

// SystemKeyer is implemented by platform input backends that can inject
// device-level keys (notably Android: HOME/BACK/APP_SWITCH/VOLUME/POWER).
// Desktop backends may map a subset to key chords.
type SystemKeyer interface {
	SystemKey(name string) error
}

// button codes on the wire (match the DOM MouseEvent.button values).
const (
	wireBtnLeft   = 0
	wireBtnMiddle = 1
	wireBtnRight  = 2
)

// modifier bits (match DOM KeyboardEvent getModifierState).
const (
	modShift = 1 << 0
	modCtrl  = 1 << 1
	modAlt   = 1 << 2
	modMeta  = 1 << 3
)

func wireButton(b uint8) MouseButton {
	switch b {
	case wireBtnRight:
		return ButtonRight
	case wireBtnMiddle:
		return ButtonMiddle
	default:
		return ButtonLeft
	}
}

// ControlHandler applies decoded remote-input events. The zero value is inert;
// SetInput wires a platform InputProvider, SetClipboard a clipboard backend.
type ControlHandler struct {
	input InputProvider
	touch TouchProvider
	clip  Clipboard
	// onHello is called with the browser's viewport size on each hello.
	onHello func(w, h int)
}

// NewControlHandler builds a handler over the given input provider (may be nil).
func NewControlHandler(input InputProvider, clip Clipboard) *ControlHandler {
	return &ControlHandler{input: input, clip: clip}
}

// TouchProvider injects native touchscreen events (finger). A platform that has
// both a touchscreen and a mouse (Android) implements this so the viewer's
// touch gestures drive the real touchscreen while the mouse drives the cursor.
type TouchProvider interface {
	TouchDown(Point) error
	TouchMove(Point) error
	TouchUp() error
}

// SetTouch wires a native touch backend (optional).
func (h *ControlHandler) SetTouch(t TouchProvider) { h.touch = t }

// SetOnHello installs a callback for viewport-size reports.
func (h *ControlHandler) SetOnHello(fn func(w, h int)) { h.onHello = fn }

// Handle decodes one control message and applies it. It returns an optional
// reply (e.g. clipboard data) that the caller should send back on the channel.
func (h *ControlHandler) Handle(msg []byte) (reply []byte, ok bool) {
	if len(msg) == 0 {
		return nil, false
	}
	typ := msg[0]
	p := msg[1:]
	switch typ {
	case ctrlMouseMove:
		if h.input == nil {
			return nil, false
		}
		x, y, ok := rdPoint(p)
		if !ok {
			return nil, false
		}
		_ = h.input.PointerMove(Point{X: x, Y: y})
	case ctrlMouseDown:
		if h.input == nil || len(p) < 9 {
			return nil, false
		}
		x, y, _ := rdPoint(p)
		_ = h.input.PointerMove(Point{X: x, Y: y})
		_ = h.input.PointerDown(wireButton(p[8]))
	case ctrlMouseUp:
		if h.input == nil || len(p) < 1 {
			return nil, false
		}
		_ = h.input.PointerUp(wireButton(p[0]))
	case ctrlTouchDown:
		if h.touch == nil || len(p) < 8 {
			return nil, false
		}
		x, y, ok := rdPoint(p)
		if !ok {
			return nil, false
		}
		_ = h.touch.TouchDown(Point{X: x, Y: y})
	case ctrlTouchMove:
		if h.touch == nil || len(p) < 8 {
			return nil, false
		}
		x, y, ok := rdPoint(p)
		if !ok {
			return nil, false
		}
		_ = h.touch.TouchMove(Point{X: x, Y: y})
	case ctrlTouchUp:
		if h.touch == nil {
			return nil, false
		}
		_ = h.touch.TouchUp()
	case ctrlWheel:
		if h.input == nil || len(p) < 16 {
			return nil, false
		}
		x := int32(binary.LittleEndian.Uint32(p[0:]))
		y := int32(binary.LittleEndian.Uint32(p[4:]))
		dx := int32(binary.LittleEndian.Uint32(p[8:]))
		dy := int32(binary.LittleEndian.Uint32(p[12:]))
		_ = h.input.PointerScroll(Point{X: x, Y: y}, ScrollDelta{LineX: dx, LineY: dy})
	case ctrlKeyDown, ctrlKeyUp:
		if h.input == nil {
			return nil, false
		}
		key, mods, ok := rdKey(p)
		if !ok {
			return nil, false
		}
		if typ == ctrlKeyDown {
			h.applyMods(mods, true)
			_ = h.input.KeyDown(key)
		} else {
			_ = h.input.KeyUp(key)
			h.applyMods(mods, false)
		}
	case ctrlText:
		if h.input == nil {
			return nil, false
		}
		_ = h.input.TypeText(string(p))
	case ctrlHello:
		if len(p) >= 8 && h.onHello != nil {
			w := int(int32(binary.LittleEndian.Uint32(p[0:])))
			hh := int(int32(binary.LittleEndian.Uint32(p[4:])))
			h.onHello(w, hh)
		}
	case ctrlClipSet:
		if h.clip != nil {
			_ = h.clip.Set(string(p))
		}
	case ctrlClipGet:
		if h.clip != nil {
			if s, err := h.clip.Get(); err == nil {
				return encodeClipData(s), true
			}
		}
	case ctrlSystemKey:
		if sk, ok := h.input.(SystemKeyer); ok && len(p) >= 1 {
			n := int(p[0])
			if len(p) >= 1+n {
				_ = sk.SystemKey(string(p[1 : 1+n]))
			}
		}
	case ctrlGesture:
		if h.input == nil || len(p) < 21 {
			return nil, false
		}
		h.handleGesture(p)
	}
	return nil, false
}

// handleGesture applies a touch-style gesture (long-press / drag / multi-step).
// kind: 0 = long-press, 1 = swipe (drag from a to b).
func (h *ControlHandler) handleGesture(p []byte) {
	kind := p[0]
	x1 := int32(binary.LittleEndian.Uint32(p[1:]))
	y1 := int32(binary.LittleEndian.Uint32(p[5:]))
	x2 := int32(binary.LittleEndian.Uint32(p[9:]))
	y2 := int32(binary.LittleEndian.Uint32(p[13:]))
	ms := int32(binary.LittleEndian.Uint32(p[17:]))
	if ms <= 0 {
		ms = 300
	}
	switch kind {
	case 0: // long-press at (x1,y1)
		_ = h.input.PointerMove(Point{X: x1, Y: y1})
		_ = h.input.PointerDown(ButtonLeft)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		_ = h.input.PointerUp(ButtonLeft)
	case 1: // drag (x1,y1) -> (x2,y2)
		_ = h.input.PointerMove(Point{X: x1, Y: y1})
		_ = h.input.PointerDown(ButtonLeft)
		_ = h.input.PointerMove(Point{X: x2, Y: y2})
		_ = h.input.PointerUp(ButtonLeft)
	}
}

// applyMods presses/releases modifier keys around a key event so shortcuts
// (Ctrl+C, Shift+Tab) work: the browser sends the modifier state per event.
func (h *ControlHandler) applyMods(mods uint8, down bool) {
	keys := []struct {
		bit  uint8
		name string
	}{
		{modCtrl, "Ctrl"},
		{modAlt, "Alt"},
		{modShift, "Shift"},
		{modMeta, "Meta"},
	}
	if down {
		for _, k := range keys {
			if mods&k.bit != 0 {
				_ = h.input.KeyDown(NamedKey(k.name))
			}
		}
	} else {
		for i := len(keys) - 1; i >= 0; i-- {
			if mods&keys[i].bit != 0 {
				_ = h.input.KeyUp(NamedKey(keys[i].name))
			}
		}
	}
}

func rdPoint(p []byte) (int32, int32, bool) {
	if len(p) < 8 {
		return 0, 0, false
	}
	return int32(binary.LittleEndian.Uint32(p[0:])), int32(binary.LittleEndian.Uint32(p[4:])), true
}

func rdKey(p []byte) (Key, uint8, bool) {
	if len(p) < 1 {
		return Key{}, 0, false
	}
	n := int(p[0])
	if len(p) < 1+n+5 {
		return Key{}, 0, false
	}
	name := string(p[1 : 1+n])
	ch := binary.LittleEndian.Uint32(p[1+n:])
	mods := p[1+n+4]
	var k Key
	if name != "" {
		k = NamedKey(name)
	} else if ch != 0 {
		k = CharKey(rune(ch))
	}
	return k, mods, true
}

// encodeClipData frames a clipboard payload for the browser.
func encodeClipData(s string) []byte {
	out := make([]byte, 1+len(s))
	out[0] = ctrlClipData
	copy(out[1:], s)
	return out
}

// DecodeControlType returns a human label for a control message (diagnostics).
func DecodeControlType(msg []byte) string {
	if len(msg) == 0 {
		return ""
	}
	switch msg[0] {
	case ctrlMouseMove:
		return "mousemove"
	case ctrlMouseDown:
		return "mousedown"
	case ctrlMouseUp:
		return "mouseup"
	case ctrlWheel:
		return "wheel"
	case ctrlKeyDown:
		return "keydown"
	case ctrlKeyUp:
		return "keyup"
	case ctrlText:
		return "text"
	case ctrlHello:
		return "hello"
	case ctrlClipSet:
		return "clip-set"
	case ctrlClipGet:
		return "clip-get"
	case ctrlClipData:
		return "clip-data"
	default:
		return "unknown"
	}
}

// KeyNameFromCode maps a DOM KeyboardEvent.code (physical key) to the named-key
// vocabulary the platform keymaps understand. It returns "" when the key is a
// printable one (the caller then uses Char instead).
func KeyNameFromCode(code string) string {
	if code == "" {
		return ""
	}
	switch code {
	case "Enter", "NumpadEnter":
		return "Return"
	case "Escape":
		return "Escape"
	case "Tab":
		return "Tab"
	case "Backspace":
		return "Backspace"
	case "Delete":
		return "Delete"
	case " ":
		return "Space"
	case "ArrowUp":
		return "Up"
	case "ArrowDown":
		return "Down"
	case "ArrowLeft":
		return "Left"
	case "ArrowRight":
		return "Right"
	case "Home":
		return "Home"
	case "End":
		return "End"
	case "PageUp":
		return "PageUp"
	case "PageDown":
		return "PageDown"
	case "ShiftLeft", "ShiftRight":
		return "Shift"
	case "ControlLeft", "ControlRight":
		return "Ctrl"
	case "AltLeft", "AltRight":
		return "Alt"
	case "MetaLeft", "MetaRight":
		return "Meta"
	}
	if strings.HasPrefix(code, "F") && len(code) <= 3 {
		return code // F1..F12
	}
	return ""
}
