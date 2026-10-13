package android

import (
	"fmt"
	"strings"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// Input injects pointer/keyboard events via `input`.
//
// Android (11+) natively supports a MOUSE source: `input mouse motionevent
// MOVE|DOWN|UP x y` produces real pointer events (hover, buttons) and
// `input mouse scroll x y --axis ...` produces wheel events. This maps 1:1 onto
// the standard pointer protocol, so a WebRTC viewer drives Android exactly like
// a desktop — no gesture translation.
//
// The last pointer position is remembered so DOWN/UP (which carry no
// coordinates of their own) act at the current location.
type Input struct {
	x, y int32
}

func NewInput() *Input { return &Input{} }

func (i *Input) Name() string { return "android-input" }

func (i *Input) PointerMove(to goa11y.Point) error {
	i.x, i.y = to.X, to.Y
	return i.mouse("motionevent", "MOVE", itoa(to.X), itoa(to.Y))
}

func (i *Input) PointerDown(b goa11y.MouseButton) error {
	return i.mouse("motionevent", "DOWN", itoa(i.x), itoa(i.y))
}

func (i *Input) PointerUp(b goa11y.MouseButton) error {
	return i.mouse("motionevent", "UP", itoa(i.x), itoa(i.y))
}

// mouse runs `input mouse <args...>` (the native mouse source).
func (i *Input) mouse(args ...string) error {
	return runInput(append([]string{"mouse"}, args...)...)
}

// Touch injects real touchscreen events (finger) via `input touchscreen`. It is
// distinct from the mouse: a viewer's finger drives the touchscreen, its mouse
// drives the pointer — both can coexist on Android.
func (i *Input) TouchDown(to goa11y.Point) error {
	return runInput("touchscreen", "motionevent", "DOWN", itoa(to.X), itoa(to.Y))
}
func (i *Input) TouchMove(to goa11y.Point) error {
	return runInput("touchscreen", "motionevent", "MOVE", itoa(to.X), itoa(to.Y))
}
func (i *Input) TouchUp() error {
	return runInput("touchscreen", "motionevent", "UP", "0", "0")
}

func (i *Input) PointerClick(at goa11y.Point, button goa11y.MouseButton, count uint32) error {
	i.x, i.y = at.X, at.Y
	if button != goa11y.ButtonLeft && button != "" {
		return goa11y.Unsupported("non-primary mouse button on Android")
	}
	for n := uint32(0); n < count; n++ {
		if err := i.mouse("motionevent", "MOVE", itoa(at.X), itoa(at.Y)); err != nil {
			return err
		}
		if err := i.mouse("motionevent", "DOWN", itoa(at.X), itoa(at.Y)); err != nil {
			return err
		}
		if err := i.mouse("motionevent", "UP", itoa(at.X), itoa(at.Y)); err != nil {
			return err
		}
	}
	return nil
}

func (i *Input) PointerScroll(at goa11y.Point, delta goa11y.ScrollDelta) error {
	i.x, i.y = at.X, at.Y
	dy := delta.PixelY + delta.LineY*40
	dx := delta.PixelX + delta.LineX*40
	args := []string{"scroll", itoa(at.X), itoa(at.Y)}
	if dy != 0 {
		args = append(args, "--axis", "VSCROLL,"+itoa(-dy))
	}
	if dx != 0 {
		args = append(args, "--axis", "HSCROLL,"+itoa(-dx))
	}
	if len(args) == 3 {
		return nil // no movement
	}
	return i.mouse(args...)
}

func (i *Input) KeyDown(key goa11y.Key) error { return i.pressKey(key) }
func (i *Input) KeyUp(key goa11y.Key) error   { return nil }

func (i *Input) pressKey(key goa11y.Key) error {
	if key.Name != "" {
		return runInput("keyevent", androidKeyName(key.Name))
	}
	if key.Char != 0 {
		return inputText(string(key.Char))
	}
	return goa11y.Unsupported("empty key")
}

func (i *Input) TypeText(text string) error { return inputText(text) }

// androidKeyName maps goa11y named keys to Android KEYCODE_* names.
func androidKeyName(name string) string {
	switch strings.ToLower(name) {
	case "return", "enter":
		return "KEYCODE_ENTER"
	case "escape", "esc":
		return "KEYCODE_ESCAPE"
	case "tab":
		return "KEYCODE_TAB"
	case "backspace":
		return "KEYCODE_DEL"
	case "delete":
		return "KEYCODE_FORWARD_DEL"
	case "space":
		return "KEYCODE_SPACE"
	case "up":
		return "KEYCODE_DPAD_UP"
	case "down":
		return "KEYCODE_DPAD_DOWN"
	case "left":
		return "KEYCODE_DPAD_LEFT"
	case "right":
		return "KEYCODE_DPAD_RIGHT"
	case "home":
		return "KEYCODE_HOME"
	case "back":
		return "KEYCODE_BACK"
	}
	if strings.HasPrefix(name, "KEYCODE_") {
		return name
	}
	return "KEYCODE_" + strings.ToUpper(name)
}

func itoa(n int32) string { return fmt.Sprintf("%d", n) }
