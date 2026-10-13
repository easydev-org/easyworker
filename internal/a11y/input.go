package goa11y

import "time"

// Point is a 2D point in desktop coordinates.
type Point struct {
	X, Y int32
}

func (p Point) ToPhysical(scale float64) Point {
	if scale <= 0 || scale == 1 {
		return p
	}
	return Point{X: int32(float64(p.X) * scale), Y: int32(float64(p.Y) * scale)}
}

func (p Point) ToLogical(scale float64) Point {
	if scale <= 0 || scale == 1 {
		return p
	}
	return Point{X: int32(float64(p.X) / scale), Y: int32(float64(p.Y) / scale)}
}

// Anchor picks a reference point within an element's bounds.
type Anchor string

const (
	AnchorCenter      Anchor = "center"
	AnchorTopLeft     Anchor = "top_left"
	AnchorTopRight    Anchor = "top_right"
	AnchorBottomLeft  Anchor = "bottom_left"
	AnchorBottomRight Anchor = "bottom_right"
)

// AnchorPoint computes the anchor point inside a rect.
func AnchorPoint(r Rect, a Anchor) Point {
	switch a {
	case AnchorTopLeft:
		return Point{X: r.X + int32(r.Width)/4, Y: r.Y + int32(r.Height)/4}
	case AnchorTopRight:
		return Point{X: r.X + int32(r.Width)*3/4, Y: r.Y + int32(r.Height)/4}
	case AnchorBottomLeft:
		return Point{X: r.X + int32(r.Width)/4, Y: r.Y + int32(r.Height)*3/4}
	case AnchorBottomRight:
		return Point{X: r.X + int32(r.Width)*3/4, Y: r.Y + int32(r.Height)*3/4}
	default: // center
		return Point{X: r.X + int32(r.Width)/2, Y: r.Y + int32(r.Height)/2}
	}
}

// PointFor computes an anchor point for an element, erroring when it has no bounds.
func PointFor(e *Element, a Anchor) (Point, error) {
	if e.data.Bounds == nil {
		return Point{}, &Error{Kind: ErrNoElementBounds}
	}
	return AnchorPoint(*e.data.Bounds, a), nil
}

// MouseButton identifies a pointer button.
type MouseButton string

const (
	ButtonLeft   MouseButton = "left"
	ButtonRight  MouseButton = "right"
	ButtonMiddle MouseButton = "middle"
)

// ScrollDelta is a scroll amount; exactly one of Pixel/Line/Page should be set.
type ScrollDelta struct {
	PixelX, PixelY int32
	LineX, LineY   int32
	PageX, PageY   int32
}

// Key is a keyboard key. Char represents the unshifted physical key.
type Key struct {
	// Char is the printable character (0 when this is a named key).
	Char rune
	// Name is the named key, e.g. "Return", "Escape", "Tab", "Shift", "Ctrl".
	Name string
}

func CharKey(c rune) Key    { return Key{Char: c} }
func NamedKey(n string) Key { return Key{Name: n} }

// InputProvider synthesizes OS-level pointer/keyboard events. This is separate
// from the a11y action layer: use it only for interactions with no a11y
// equivalent (drag, scroll, complex shortcuts).
type InputProvider interface {
	PointerMove(to Point) error
	PointerDown(button MouseButton) error
	PointerUp(button MouseButton) error
	PointerClick(at Point, button MouseButton, count uint32) error
	PointerScroll(at Point, delta ScrollDelta) error
	KeyDown(key Key) error
	KeyUp(key Key) error
	TypeText(text string) error
	Name() string
}

// InputSim wraps an InputProvider with convenience Mouse/Keyboard handles.
type InputSim struct {
	backend InputProvider
}

func NewInputSim(b InputProvider) *InputSim { return &InputSim{backend: b} }
func (s *InputSim) Backend() InputProvider  { return s.backend }
func (s *InputSim) Mouse() *Mouse           { return &Mouse{sim: s} }
func (s *InputSim) Keyboard() *Keyboard     { return &Keyboard{sim: s} }

// Mouse provides pointer operations.
type Mouse struct{ sim *InputSim }

func (m *Mouse) Click(at Point) error { return m.sim.backend.PointerClick(at, ButtonLeft, 1) }
func (m *Mouse) ClickWith(at Point, button MouseButton, count uint32) error {
	return m.sim.backend.PointerClick(at, button, count)
}
func (m *Mouse) DoubleClick(at Point) error { return m.sim.backend.PointerClick(at, ButtonLeft, 2) }
func (m *Mouse) RightClick(at Point) error  { return m.sim.backend.PointerClick(at, ButtonRight, 1) }
func (m *Mouse) Down(b MouseButton) error   { return m.sim.backend.PointerDown(b) }
func (m *Mouse) Up(b MouseButton) error     { return m.sim.backend.PointerUp(b) }
func (m *Mouse) MoveTo(at Point) error      { return m.sim.backend.PointerMove(at) }
func (m *Mouse) Drag(from, to Point) error  { return m.drag(from, to, ButtonLeft, 0) }

func (m *Mouse) drag(from, to Point, button MouseButton, dur time.Duration) error {
	if err := m.sim.backend.PointerMove(from); err != nil {
		return err
	}
	if err := m.sim.backend.PointerDown(button); err != nil {
		return err
	}
	steps := 20
	if dur > 0 {
		steps = int(dur / (10 * time.Millisecond))
		if steps < 1 {
			steps = 1
		}
	}
	for i := 1; i <= steps; i++ {
		px := from.X + (to.X-from.X)*int32(i)/int32(steps)
		py := from.Y + (to.Y-from.Y)*int32(i)/int32(steps)
		if err := m.sim.backend.PointerMove(Point{X: px, Y: py}); err != nil {
			return err
		}
	}
	return m.sim.backend.PointerUp(button)
}

func (m *Mouse) Scroll(at Point, delta ScrollDelta) error {
	return m.sim.backend.PointerScroll(at, delta)
}

// Keyboard provides key operations.
type Keyboard struct{ sim *InputSim }

func (k *Keyboard) Press(key Key) error             { return k.press(key, nil) }
func (k *Keyboard) Chord(key Key, held []Key) error { return k.press(key, held) }
func (k *Keyboard) Down(key Key) error              { return k.sim.backend.KeyDown(key) }
func (k *Keyboard) Up(key Key) error                { return k.sim.backend.KeyUp(key) }
func (k *Keyboard) TypeText(text string) error      { return k.sim.backend.TypeText(text) }

func (k *Keyboard) press(key Key, held []Key) error {
	for _, h := range held {
		if err := k.sim.backend.KeyDown(h); err != nil {
			return err
		}
	}
	if err := k.sim.backend.KeyDown(key); err != nil {
		return err
	}
	if err := k.sim.backend.KeyUp(key); err != nil {
		return err
	}
	for i := len(held) - 1; i >= 0; i-- {
		if err := k.sim.backend.KeyUp(held[i]); err != nil {
			return err
		}
	}
	return nil
}
