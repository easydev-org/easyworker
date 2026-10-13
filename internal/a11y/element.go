package goa11y

// Rect is a rectangle in desktop coordinates (physical pixels on Windows,
// logical points on macOS/Linux).
type Rect struct {
	X, Y          int32
	Width, Height uint32
}

func (r Rect) ToPhysical(scale float64) Rect {
	if scale <= 0 || scale == 1 {
		return r
	}
	return Rect{
		X: int32(float64(r.X) * scale), Y: int32(float64(r.Y) * scale),
		Width: uint32(float64(r.Width) * scale), Height: uint32(float64(r.Height) * scale),
	}
}

func (r Rect) ToLogical(scale float64) Rect {
	if scale <= 0 || scale == 1 {
		return r
	}
	return Rect{
		X: int32(float64(r.X) / scale), Y: int32(float64(r.Y) / scale),
		Width: uint32(float64(r.Width) / scale), Height: uint32(float64(r.Height) / scale),
	}
}

// Toggled is a tri-state checkbox value.
type Toggled int8

const (
	ToggledUnset Toggled = iota
	ToggledOff
	ToggledOn
	ToggledMixed
)

// StateSet carries an element's accessibility states.
type StateSet struct {
	Enabled   bool
	Visible   bool
	Focused   bool
	Active    bool
	Selected  bool
	Editable  bool
	Focusable bool
	Modal     bool
	Required  bool
	Busy      bool

	Minimized  *bool
	Maximized  *bool
	Fullscreen *bool
	Expanded   *bool
	Checked    Toggled
}

// RawPlatformData holds provider-specific attributes (e.g. platform handles).
type RawPlatformData map[string]any

// ElementData is the platform-independent snapshot of one accessibility node.
// Handle is a provider-specific opaque id used to look up the underlying
// platform object; Raw carries extra provider attributes.
type ElementData struct {
	Role         Role
	Name         string
	Value        string
	Description  string
	Bounds       *Rect
	Actions      []string
	States       StateSet
	NumericValue *float64
	MinValue     *float64
	MaxValue     *float64
	StableID     string
	PID          *uint32
	Raw          RawPlatformData

	// Handle is the provider-specific identity (window handle, AXUIElement
	// pointer, AT-SPI object path hash). Set by the provider.
	Handle uint64
}

// Element is a live handle to a tree node bound to its provider.
type Element struct {
	data     ElementData
	provider Provider
}

func NewElement(data ElementData, p Provider) *Element {
	return &Element{data: data, provider: p}
}

func (e *Element) Data() *ElementData { return &e.data }
func (e *Element) Provider() Provider { return e.provider }
func (e *Element) Role() Role         { return e.data.Role }
func (e *Element) Name() string       { return e.data.Name }
func (e *Element) Bounds() *Rect      { return e.data.Bounds }

func (e *Element) Children() ([]*Element, error) {
	kids, err := e.provider.GetChildren(&e.data)
	if err != nil {
		return nil, err
	}
	return wrap(e.provider, kids), nil
}

func (e *Element) Parent() (*Element, error) {
	p, err := e.provider.GetParent(&e.data)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return NewElement(*p, e.provider), nil
}

func (e *Element) PID() (uint32, bool) {
	if e.data.PID == nil {
		return 0, false
	}
	return *e.data.PID, true
}

func (e *Element) Dump(maxDepth *int) (string, error) {
	return dumpTree(e.provider, &e.data, maxDepth)
}

// Actions (a11y actions)
func (e *Element) Press() error           { return e.provider.Press(&e.data) }
func (e *Element) Focus() error           { return e.provider.Focus(&e.data) }
func (e *Element) Blur() error            { return e.provider.Blur(&e.data) }
func (e *Element) Toggle() error          { return e.provider.Toggle(&e.data) }
func (e *Element) Select() error          { return e.provider.Select(&e.data) }
func (e *Element) Expand() error          { return e.provider.Expand(&e.data) }
func (e *Element) Collapse() error        { return e.provider.Collapse(&e.data) }
func (e *Element) ShowMenu() error        { return e.provider.ShowMenu(&e.data) }
func (e *Element) Increment() error       { return e.provider.Increment(&e.data) }
func (e *Element) Decrement() error       { return e.provider.Decrement(&e.data) }
func (e *Element) ScrollIntoView() error  { return e.provider.ScrollIntoView(&e.data) }
func (e *Element) SetValue(v string) error { return e.provider.SetValue(&e.data, v) }
func (e *Element) SetNumericValue(v float64) error { return e.provider.SetNumericValue(&e.data, v) }
func (e *Element) SetTextSelection(start, end uint32) error {
	return e.provider.SetTextSelection(&e.data, start, end)
}
func (e *Element) TypeText(t string) error { return e.provider.TypeText(&e.data, t) }
func (e *Element) PerformAction(a string) error { return e.provider.PerformAction(&e.data, a) }
func (e *Element) Activate() error        { return e.provider.Activate(&e.data) }
func (e *Element) Minimize() error        { return e.provider.Minimize(&e.data) }
func (e *Element) Maximize() error        { return e.provider.Maximize(&e.data) }
func (e *Element) EnterFullscreen() error { return e.provider.EnterFullscreen(&e.data) }
func (e *Element) Restore() error         { return e.provider.Restore(&e.data) }
func (e *Element) Close() error           { return e.provider.Close(&e.data) }
func (e *Element) MoveTo(x, y int32) error { return e.provider.MoveTo(&e.data, x, y) }
func (e *Element) ResizeTo(w, h uint32) error { return e.provider.ResizeTo(&e.data, w, h) }

func wrap(p Provider, ds []ElementData) []*Element {
	out := make([]*Element, len(ds))
	for i := range ds {
		out[i] = NewElement(ds[i], p)
	}
	return out
}

// TreeNode is a serializable tree snapshot.
type TreeNode struct {
	Role     string      `json:"role"`
	Name     string      `json:"name,omitempty"`
	Value    string      `json:"value,omitempty"`
	Children []*TreeNode `json:"children,omitempty"`
}

// MaxTreeDepth bounds traversal to avoid cycles in broken trees.
const MaxTreeDepth = 50
