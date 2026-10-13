// Package android implements the goa11y Provider/InputProvider/ScreenshotProvider
// for Android by wrapping the platform's own tools (uiautomator, input,
// screencap) as subprocesses. No CGO/NDK required: the binary is a static Go
// program that runs *inside* the Android guest.
//
// Design notes:
//   - The accessibility tree comes from `uiautomator dump` (UI Automator XML).
//     The dump is slow (the tool boots a Java process each call), so results
//     are cached briefly and the caller can refresh explicitly.
//   - Input is injected with `input tap/swipe/text/keyevent`, which routes
//     through Android's InputManager (same as a real touchscreen).
//   - Screenshots come from `screencap -p` (SurfaceFlinger), PNG on stdout.
package android

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// Provider is the Android accessibility backend.
type Provider struct {
	mu       sync.Mutex
	roots    []*node
	cacheAt  time.Time
	cacheTTL time.Duration
}

func New() *Provider { return &Provider{cacheTTL: 2 * time.Second} }

func (p *Provider) Name() string { return "android-uiautomator" }

// ── tree source ────────────────────────────────────────────────────

func (p *Provider) refresh() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.roots != nil && time.Since(p.cacheAt) < p.cacheTTL {
		return nil
	}
	xml, err := dumpUI()
	if err != nil {
		return err
	}
	roots, err := parseHierarchy(xml)
	if err != nil {
		return err
	}
	p.roots = roots
	p.cacheAt = time.Now()
	return nil
}

func dumpUI() ([]byte, error) {
	// `uiautomator dump /dev/tty` writes the XML to stdout on modern Android;
	// fall back to dumping to a file and reading it.
	cmd := exec.Command("uiautomator", "dump", "/dev/tty")
	out, err := cmd.Output()
	if err == nil && bytes.Contains(out, []byte("<hierarchy")) {
		return out, nil
	}
	// Fallback: dump to a file, read it back.
	tmp := "/data/local/tmp/.goa11y_ui.xml"
	if e := exec.Command("uiautomator", "dump", tmp).Run(); e != nil {
		return nil, goa11y.PlatformErr(-1, "uiautomator dump failed: "+e.Error())
	}
	return exec.Command("cat", tmp).Output()
}

// ── Provider impl ──────────────────────────────────────────────────

func (p *Provider) GetChildren(e *goa11y.ElementData) ([]goa11y.ElementData, error) {
	if err := p.refresh(); err != nil {
		return nil, err
	}
	if e == nil {
		// top-level: return the apps (by package) — one synthetic node each.
		return p.listAppsLocked(), nil
	}
	n := p.lookup(e.Handle)
	if n == nil {
		return nil, &goa11y.Error{Kind: goa11y.ErrElementStale, Selector: e.Name}
	}
	out := make([]goa11y.ElementData, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c.toData())
	}
	return out, nil
}

func (p *Provider) GetParent(e *goa11y.ElementData) (*goa11y.ElementData, error) {
	if err := p.refresh(); err != nil {
		return nil, err
	}
	n := p.lookup(e.Handle)
	if n == nil || n.parent == nil {
		return nil, nil
	}
	d := n.parent.toData()
	return &d, nil
}

func (p *Provider) ListApps() ([]goa11y.ElementData, error) {
	if err := p.refresh(); err != nil {
		return nil, err
	}
	return p.listAppsLocked(), nil
}

func (p *Provider) listAppsLocked() []goa11y.ElementData {
	// Group top-level nodes by package → one Application element per package.
	byPkg := map[string][]*node{}
	var order []string
	for _, r := range p.roots {
		if _, ok := byPkg[r.pkg]; !ok {
			order = append(order, r.pkg)
		}
		byPkg[r.pkg] = append(byPkg[r.pkg], r)
	}
	out := make([]goa11y.ElementData, 0, len(order))
	for _, pkg := range order {
		nodes := byPkg[pkg]
		d := nodes[0].toData()
		d.Role = goa11y.RoleApplication
		d.Name = pkg
		d.Raw["package"] = pkg
		out = append(out, d)
	}
	return out
}

func (p *Provider) AppWindows(app *goa11y.ElementData) ([]goa11y.ElementData, error) {
	return p.GetChildren(app)
}

func (p *Provider) AppByPID(pid uint32) (*goa11y.ElementData, error) {
	return nil, goa11y.Unsupported("AppByPID on Android")
}

func (p *Provider) FocusedApp() (*goa11y.ElementData, error) {
	if err := p.refresh(); err != nil {
		return nil, err
	}
	apps := p.listAppsLocked()
	if len(apps) == 0 {
		return nil, goa11y.SelectorNotMatched("focused app")
	}
	d := apps[0]
	return &d, nil
}

// ── Actions (a11y) ─────────────────────────────────────────────────

func (p *Provider) Press(e *goa11y.ElementData) error {
	return p.tapCenter(e)
}
func (p *Provider) Focus(e *goa11y.ElementData) error   { return p.tapCenter(e) }
func (p *Provider) Blur(e *goa11y.ElementData) error    { return nil }
func (p *Provider) Toggle(e *goa11y.ElementData) error  { return p.tapCenter(e) }
func (p *Provider) Select(e *goa11y.ElementData) error  { return p.tapCenter(e) }
func (p *Provider) Expand(e *goa11y.ElementData) error  { return p.tapCenter(e) }
func (p *Provider) Collapse(e *goa11y.ElementData) error { return p.tapCenter(e) }
func (p *Provider) ShowMenu(e *goa11y.ElementData) error { return p.longPress(e) }
func (p *Provider) Increment(e *goa11y.ElementData) error { return goa11y.Unsupported("increment") }
func (p *Provider) Decrement(e *goa11y.ElementData) error { return goa11y.Unsupported("decrement") }
func (p *Provider) ScrollIntoView(e *goa11y.ElementData) error { return goa11y.Unsupported("scrollIntoView") }
func (p *Provider) SetNumericValue(e *goa11y.ElementData, v float64) error {
	return goa11y.Unsupported("setNumericValue")
}
func (p *Provider) PerformAction(e *goa11y.ElementData, a string) error {
	return goa11y.Unsupported("action " + a)
}
func (p *Provider) Activate(e *goa11y.ElementData) error { return p.tapCenter(e) }
func (p *Provider) Minimize(e *goa11y.ElementData) error { return goa11y.Unsupported("minimize") }
func (p *Provider) Maximize(e *goa11y.ElementData) error { return goa11y.Unsupported("maximize") }
func (p *Provider) EnterFullscreen(e *goa11y.ElementData) error { return goa11y.Unsupported("fullscreen") }
func (p *Provider) Restore(e *goa11y.ElementData) error  { return goa11y.Unsupported("restore") }
func (p *Provider) Close(e *goa11y.ElementData) error    { return goa11y.Unsupported("close") }
func (p *Provider) MoveTo(e *goa11y.ElementData, x, y int32) error { return goa11y.Unsupported("moveTo") }
func (p *Provider) ResizeTo(e *goa11y.ElementData, w, h uint32) error { return goa11y.Unsupported("resizeTo") }

func (p *Provider) SetValue(e *goa11y.ElementData, value string) error {
	// Focus the field, then type.
	if err := p.tapCenter(e); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	return inputText(value)
}

func (p *Provider) TypeText(e *goa11y.ElementData, text string) error {
	if err := p.tapCenter(e); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	return inputText(text)
}

// ── helpers ────────────────────────────────────────────────────────

func (p *Provider) tapCenter(e *goa11y.ElementData) error {
	if e.Bounds == nil {
		return &goa11y.Error{Kind: goa11y.ErrNoElementBounds}
	}
	cx := e.Bounds.X + int32(e.Bounds.Width)/2
	cy := e.Bounds.Y + int32(e.Bounds.Height)/2
	return runInput("tap", fmt.Sprintf("%d", cx), fmt.Sprintf("%d", cy))
}

func (p *Provider) longPress(e *goa11y.ElementData) error {
	if e.Bounds == nil {
		return &goa11y.Error{Kind: goa11y.ErrNoElementBounds}
	}
	cx := e.Bounds.X + int32(e.Bounds.Width)/2
	cy := e.Bounds.Y + int32(e.Bounds.Height)/2
	return runInput("swipe", fmt.Sprintf("%d", cx), fmt.Sprintf("%d", cy),
		fmt.Sprintf("%d", cx), fmt.Sprintf("%d", cy), "600")
}

func (p *Provider) lookup(handle uint64) *node {
	for _, r := range p.roots {
		if n := r.find(handle); n != nil {
			return n
		}
	}
	return nil
}

func runInput(args ...string) error {
	full := append([]string{"input"}, args...)
	out, err := exec.Command(full[0], full[1:]...).CombinedOutput()
	if err != nil {
		return goa11y.PlatformErr(-1, fmt.Sprintf("input %s failed: %v: %s",
			strings.Join(args, " "), err, string(out)))
	}
	return nil
}

func inputText(text string) error {
	// `input text` treats spaces specially; percent-encode them.
	esc := strings.ReplaceAll(text, " ", "%s")
	return runInput("text", esc)
}

// SetTextSelection is unsupported on Android (use text input instead).
func (p *Provider) SetTextSelection(e *goa11y.ElementData, start, end uint32) error {
	return goa11y.Unsupported("setTextSelection on Android")
}
