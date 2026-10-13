package internal

import "testing"

func TestProbeCapabilitiesDesktop(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if c := probeCapabilities(); c.Desktop || c.Display != "" {
		t.Fatalf("no display must yield none, got %+v", c)
	}
	t.Setenv("DISPLAY", ":99")
	if c := probeCapabilities(); !c.Desktop || c.Display != "x11" {
		t.Fatalf("DISPLAY must yield x11 desktop, got %+v", c)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if c := probeCapabilities(); !c.Desktop || c.Display != "wayland" {
		t.Fatalf("WAYLAND_DISPLAY must yield wayland desktop, got %+v", c)
	}
}
