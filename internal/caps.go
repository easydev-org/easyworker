package internal

import (
	"os"
	"os/exec"

	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
)

// probeCapabilities reports what the sandbox IMAGE can do. Everything is
// probed at request time (env, PATH, a TCP dial) so nothing is baked into the
// binary and a new toolchain/desktop image needs no worker change. It is
// best-effort: a false value means "not detected", never "impossible".
func probeCapabilities() *workerv1.Capabilities {
	c := &workerv1.Capabilities{Distro: distroID()}

	// Desktop: X11 (DISPLAY) or Wayland (WAYLAND_DISPLAY). Jobs inherit the
	// worker's env, so the same vars reach them.
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		c.Desktop, c.Display = true, "wayland"
	case os.Getenv("DISPLAY") != "":
		c.Desktop, c.Display = true, "x11"
	}
	// VM / device sandboxes have a screen even without DISPLAY/WAYLAND_DISPLAY:
	// a macOS/Windows guest or the Android emulator. The
	// worker captures it (its computer driver) and serves it via /stream/, so
	// report `desktop` and the control panel shows the Desktop pane.
	if !c.Desktop {
		switch envOS() {
		case "darwin", "macos", "windows", "android":
			c.Desktop, c.Display = true, envOS()
		}
	}

	// xa11y: the accessibility CLI on PATH (X11 native-app automation).
	if _, err := exec.LookPath("xa11y"); err == nil {
		c.Xa11Y = true
	}

	// GUI platform + ComputerService availability. Android is declared by
	// WORKER_OS (the worker binary runs on the host OS); the tooling probe is
	// best-effort. `computer` is true when the platform has a usable driver.
	plat := envOS()
	switch plat {
	case "android":
		c.Platform = "android"
		c.Computer = hasAnyTool("adb")
	case "windows":
		c.Platform = "windows"
		c.Computer = hasAnyTool("xa11y")
	case "darwin", "macos":
		c.Platform = "macos"
		c.Computer = hasAnyTool("xa11y")
	default:
		if c.Display != "" {
			c.Platform = "linux-" + c.Display
		} else {
			c.Platform = "linux"
		}
		c.Computer = c.Xa11Y
	}
	return c
}

// hasAnyTool reports whether any of the named binaries is on PATH.
func hasAnyTool(names ...string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}
