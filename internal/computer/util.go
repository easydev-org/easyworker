package computer

import (
	"regexp"
	"strconv"

	a11y "github.com/easydev-org/easyworker/internal/a11y"
)

// regexpWxH matches a "WxH" size in a tool's stdout (e.g. xa11y prints
// `wrote PATH (WxH @Nx)`).
var regexpWxH = regexp.MustCompile(`\((\d+)x(\d+)`)

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// regexpXYWH matches the documented region form `x,y,w,h` (spaces allowed).
var regexpXYWH = regexp.MustCompile(`^(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)$`)

// grimRegion converts the API's `x,y,w,h` region into grim's `WxH+X+Y` geometry.
// A value already in grim's form (or unparseable) is returned unchanged, so an
// invalid region fails loudly instead of capturing the wrong area.
func grimRegion(region string) string {
	m := regexpXYWH.FindStringSubmatch(region)
	if m == nil {
		return region
	}
	return m[3] + "x" + m[4] + "+" + m[1] + "+" + m[2]
}

// parseRegion parses the API's `x,y,w,h` region into an a11y.Rect.
func parseRegion(region string) (a11y.Rect, bool) {
	m := regexpXYWH.FindStringSubmatch(region)
	if m == nil {
		return a11y.Rect{}, false
	}
	return a11y.Rect{X: int32(atoi(m[1])), Y: int32(atoi(m[2])), Width: uint32(atoi(m[3])), Height: uint32(atoi(m[4]))}, true
}
