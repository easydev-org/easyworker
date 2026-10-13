package android

import (
	"bytes"
	"os/exec"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// Screenshot captures the screen via `screencap -p` (SurfaceFlinger).
type Screenshot struct{}

func NewScreenshot() *Screenshot { return &Screenshot{} }

func (s *Screenshot) Name() string { return "android-screencap" }

// CaptureFull returns the whole-screen PNG bytes.
func (s *Screenshot) CaptureFull() (*goa11y.Screenshot, error) {
	out, err := exec.Command("screencap", "-p").Output()
	if err != nil {
		return nil, goa11y.PlatformErr(-1, "screencap failed: "+err.Error())
	}
	// screencap may emit CRLF line endings before the PNG signature.
	out = bytes.TrimLeft(out, "\r\n")
	return &goa11y.Screenshot{Format: "png", Data: out}, nil
}

// CaptureRegion captures a screen region. Android's screencap has no region
// flag, so callers crop the full capture instead.
func (s *Screenshot) CaptureRegion(r goa11y.Rect) (*goa11y.Screenshot, error) {
	return nil, goa11y.Unsupported("region capture on Android (crop the full capture)")
}
