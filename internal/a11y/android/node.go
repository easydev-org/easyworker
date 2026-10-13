package android

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// xmlNode mirrors a <node> element from uiautomator's UI Automator XML.
type xmlNode struct {
	Index       int       `xml:"index,attr"`
	Text        string    `xml:"text,attr"`
	ResourceID  string    `xml:"resource-id,attr"`
	Class       string    `xml:"class,attr"`
	Package     string    `xml:"package,attr"`
	ContentDesc string    `xml:"content-desc,attr"`
	Checkable   bool      `xml:"checkable,attr"`
	Checked     bool      `xml:"checked,attr"`
	Clickable   bool      `xml:"clickable,attr"`
	Enabled     bool      `xml:"enabled,attr"`
	Focusable   bool      `xml:"focusable,attr"`
	Focused     bool      `xml:"focused,attr"`
	Scrollable  bool      `xml:"scrollable,attr"`
	LongClick   bool      `xml:"long-clickable,attr"`
	Password    bool      `xml:"password,attr"`
	Selected    bool      `xml:"selected,attr"`
	Bounds      string    `xml:"bounds,attr"`
	Children    []xmlNode `xml:"node"`
}

type xmlHierarchy struct {
	Rotation int       `xml:"rotation,attr"`
	Nodes    []xmlNode `xml:"node"`
}

// node is a parsed accessibility node with a stable handle for lookup.
type node struct {
	handle   uint64
	index    int
	text     string
	resID    string
	class    string
	pkg      string
	desc     string
	bounds   *goa11y.Rect
	clickable, enabled, focusable, focused, scrollable, selected, checkable, checked, password, longClick bool
	parent   *node
	children []*node
}

func parseHierarchy(data []byte) ([]*node, error) {
	var h xmlHierarchy
	if err := xml.Unmarshal(data, &h); err != nil {
		return nil, goa11y.PlatformErr(-1, "parse uiautomator XML: "+err.Error())
	}
	var roots []*node
	var counter uint64
	var walk func(x *xmlNode, parent *node) *node
	walk = func(x *xmlNode, parent *node) *node {
		counter++
		n := &node{
			handle:    counter,
			index:     x.Index,
			text:      x.Text,
			resID:     x.ResourceID,
			class:     x.Class,
			pkg:       x.Package,
			desc:      x.ContentDesc,
			clickable: x.Clickable, enabled: x.Enabled, focusable: x.Focusable,
			focused: x.Focused, scrollable: x.Scrollable, selected: x.Selected,
			checkable: x.Checkable, checked: x.Checked, password: x.Password,
			longClick: x.LongClick,
			parent:    parent,
		}
		if r, err := parseBounds(x.Bounds); err == nil {
			n.bounds = &r
		}
		for i := range x.Children {
			n.children = append(n.children, walk(&x.Children[i], n))
		}
		return n
	}
	for i := range h.Nodes {
		roots = append(roots, walk(&h.Nodes[i], nil))
	}
	return roots, nil
}

// parseBounds parses "[x1,y1][x2,y2]".
func parseBounds(s string) (goa11y.Rect, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return goa11y.Rect{}, fmt.Errorf("bad bounds %q", s)
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, "][")
	if len(parts) != 2 {
		return goa11y.Rect{}, fmt.Errorf("bad bounds %q", s)
	}
	xy1 := strings.Split(parts[0], ",")
	xy2 := strings.Split(parts[1], ",")
	if len(xy1) != 2 || len(xy2) != 2 {
		return goa11y.Rect{}, fmt.Errorf("bad bounds %q", s)
	}
	x1, e1 := strconv.Atoi(strings.TrimSpace(xy1[0]))
	y1, e2 := strconv.Atoi(strings.TrimSpace(xy1[1]))
	x2, e3 := strconv.Atoi(strings.TrimSpace(xy2[0]))
	y2, e4 := strconv.Atoi(strings.TrimSpace(xy2[1]))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return goa11y.Rect{}, fmt.Errorf("bad bounds %q", s)
	}
	return goa11y.Rect{X: int32(x1), Y: int32(y1), Width: uint32(x2 - x1), Height: uint32(y2 - y1)}, nil
}

func (n *node) find(handle uint64) *node {
	if n.handle == handle {
		return n
	}
	for _, c := range n.children {
		if r := c.find(handle); r != nil {
			return r
		}
	}
	return nil
}

func (n *node) toData() goa11y.ElementData {
	name := n.text
	if name == "" {
		name = n.desc
	}
	d := goa11y.ElementData{
		Role:    classToRole(n.class),
		Name:    name,
		Value:   n.text,
		Bounds:  n.bounds,
		Handle:  n.handle,
		Raw:     goa11y.RawPlatformData{},
		Actions: n.actions(),
	}
	if n.resID != "" {
		d.StableID = n.resID
		d.Raw["resource_id"] = n.resID
	}
	d.Raw["class"] = n.class
	d.Raw["package"] = n.pkg
	d.States = goa11y.StateSet{
		Enabled: n.enabled, Visible: n.bounds != nil && n.bounds.Width > 0,
		Focused: n.focused, Focusable: n.focusable, Selected: n.selected,
	}
	if n.checkable {
		if n.checked {
			d.States.Checked = goa11y.ToggledOn
		} else {
			d.States.Checked = goa11y.ToggledOff
		}
	}
	return d
}

func (n *node) actions() []string {
	var a []string
	if n.clickable {
		a = append(a, "press")
	}
	if n.longClick {
		a = append(a, "show_menu")
	}
	if n.scrollable {
		a = append(a, "scroll")
	}
	if n.focusable {
		a = append(a, "focus")
	}
	return a
}

// classToRole maps an Android widget class name to a normalized goa11y Role.
func classToRole(class string) goa11y.Role {
	// Strip package prefix noise; keep the last segment.
	simple := class
	if i := strings.LastIndexByte(class, '.'); i >= 0 {
		simple = class[i+1:]
	}
	switch simple {
	case "TextView":
		return goa11y.RoleStaticText
	case "EditText", "AutoCompleteTextView", "TextInputEditText":
		return goa11y.RoleTextField
	case "Button", "AppCompatButton", "MaterialButton", "FloatingActionButton":
		return goa11y.RoleButton
	case "CheckBox", "CheckedTextView", "AppCompatCheckBox":
		return goa11y.RoleCheckBox
	case "RadioButton", "AppCompatRadioButton":
		return goa11y.RoleRadioButton
	case "Switch", "SwitchCompat", "ToggleButton":
		return goa11y.RoleSwitch
	case "Spinner", "AppCompatSpinner":
		return goa11y.RoleComboBox
	case "ImageView", "ImageButton":
		return goa11y.RoleImage
	case "ListView", "RecyclerView", "GridView", "ScrollView", "NestedScrollView", "HorizontalScrollView":
		return goa11y.RoleList
	case "SeekBar", "RatingBar":
		return goa11y.RoleSlider
	case "ProgressBar":
		return goa11y.RoleProgressBar
	case "WebView":
		return goa11y.RoleWebArea
	case "ScrollBar":
		return goa11y.RoleScrollBar
	case "Toolbar", "ActionBar":
		return goa11y.RoleToolbar
	case "FrameLayout", "LinearLayout", "RelativeLayout", "ConstraintLayout",
		"ViewGroup", "View", "CardView", "CoordinatorLayout", "AppBarLayout",
		"MaterialCardView", "TabLayout":
		return goa11y.RoleGroup
	case "Dialog":
		return goa11y.RoleDialog
	case "TabWidget", "TabHost":
		return goa11y.RoleTabGroup
	case "ViewPager", "ViewPager2":
		return goa11y.RoleTabGroup
	}
	// Heuristics on suffix.
	switch {
	case strings.HasSuffix(simple, "Layout"), strings.HasSuffix(simple, "Group"),
		strings.HasSuffix(simple, "Container"):
		return goa11y.RoleGroup
	case strings.HasSuffix(simple, "Button"):
		return goa11y.RoleButton
	case strings.HasSuffix(simple, "TextView"):
		return goa11y.RoleStaticText
	}
	return goa11y.UnknownRole(class)
}
