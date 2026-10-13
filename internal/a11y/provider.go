package goa11y

import (
	"fmt"
	"strings"
)

// Provider is the platform backend for accessibility-tree access. Each backend
// (Linux AT-SPI2, Windows UIA, macOS AX, and per-platform stubs) implements it.
// Providers do lazy, on-demand tree navigation; elements are identified by
// their ElementData (which carries a provider-specific Handle).
type Provider interface {
	// ── Tree navigation ──
	GetChildren(element *ElementData) ([]ElementData, error)
	GetParent(element *ElementData) (*ElementData, error)
	// ListApps enumerates top-level applications (role=Application).
	ListApps() ([]ElementData, error)
	// AppWindows lists an application's windows.
	AppWindows(app *ElementData) ([]ElementData, error)
	// AppByPID resolves an application element from a process id.
	AppByPID(pid uint32) (*ElementData, error)
	// FocusedApp returns the currently focused application.
	FocusedApp() (*ElementData, error)

	// ── Actions ──
	Press(element *ElementData) error
	Focus(element *ElementData) error
	Blur(element *ElementData) error
	Toggle(element *ElementData) error
	Select(element *ElementData) error
	Expand(element *ElementData) error
	Collapse(element *ElementData) error
	ShowMenu(element *ElementData) error
	Increment(element *ElementData) error
	Decrement(element *ElementData) error
	ScrollIntoView(element *ElementData) error
	SetValue(element *ElementData, value string) error
	SetNumericValue(element *ElementData, value float64) error
	SetTextSelection(element *ElementData, start, end uint32) error
	TypeText(element *ElementData, text string) error
	PerformAction(element *ElementData, action string) error

	// ── Window management ──
	Activate(element *ElementData) error
	Minimize(element *ElementData) error
	Maximize(element *ElementData) error
	EnterFullscreen(element *ElementData) error
	Restore(element *ElementData) error
	Close(element *ElementData) error
	MoveTo(element *ElementData, x, y int32) error
	ResizeTo(element *ElementData, width, height uint32) error

	// ── Metadata ──
	// Name identifies the backend, e.g. "linux-atspi", "windows-uia".
	Name() string
}

// ShellSurfaceKind classifies OS shell surfaces (taskbar, dock, panel, ...).
type ShellSurfaceKind string

const (
	ShellMenuBar     ShellSurfaceKind = "menu_bar"
	ShellStatusItems ShellSurfaceKind = "status_items"
	ShellTaskbar     ShellSurfaceKind = "taskbar"
	ShellPanel       ShellSurfaceKind = "panel"
	ShellDock        ShellSurfaceKind = "dock"
	ShellDesktop     ShellSurfaceKind = "desktop"
	ShellFlyout      ShellSurfaceKind = "flyout"
	ShellUnknown     ShellSurfaceKind = "unknown"
)

// ShellSurfaceProvider is optionally implemented by backends that can
// enumerate OS shell surfaces.
type ShellSurfaceProvider interface {
	ListShellSurfaces() ([]ShellSurface, error)
}

// ShellSurface wraps a real platform element that is an OS shell surface.
type ShellSurface struct {
	Kind    ShellSurfaceKind
	Element *Element
}

// dumpTree renders a bounded indented tree dump for diagnostics.
func dumpTree(p Provider, root *ElementData, maxDepth *int) (string, error) {
	depth := MaxTreeDepth
	if maxDepth != nil && *maxDepth >= 0 {
		depth = *maxDepth
	}
	var b strings.Builder
	var walk func(d *ElementData, level int) error
	walk = func(d *ElementData, level int) error {
		indent := strings.Repeat("  ", level)
		fmt.Fprintf(&b, "%s%s", indent, d.Role)
		if d.Name != "" {
			fmt.Fprintf(&b, " %q", d.Name)
		}
		b.WriteByte('\n')
		if level >= depth {
			return nil
		}
		kids, err := p.GetChildren(d)
		if err != nil {
			return err
		}
		for i := range kids {
			if err := walk(&kids[i], level+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}
