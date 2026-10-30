// Package computer is the worker-native GUI-automation layer. It replaces the
// client-side command assembly that used to live in the workspace extension:
// the worker knows its own platform and picks a Driver, so the client stays
// platform-agnostic (see the ComputerService RPC).
package computer

import (
	"context"
	"os"
	"runtime"
	"strings"
)

// App is one running GUI application (Android: the foreground package).
type App struct {
	PID     string
	Name    string
	Focused bool
}

// Driver is the platform-specific automation backend. Every method maps one
// logical operation onto the platform's tools (xa11y / wtype / grim / adb).
// Implementations are stateless except for the command runner.
type Driver interface {
	Platform() string // linux-x11 | linux-wayland | windows | macos | android
	Display() string  // x11 | wayland | android | ""
	Tools() []string  // tools present on PATH (xa11y, grim, adb, ...)
	Apps(ctx context.Context) ([]App, error)
	Snapshot(ctx context.Context, app string, depth int) (string, error)
	Find(ctx context.Context, selector, app, output string) (string, error)
	Action(ctx context.Context, action, target, value, app string) error
	Click(ctx context.Context, x, y int, button string, count int) error
	Type(ctx context.Context, text, target string) error
	Key(ctx context.Context, key string, held []string) error
	Scroll(ctx context.Context, x, y, dx, dy int) error
	Drag(ctx context.Context, fromX, fromY, toX, toY, durationMs int) error
	Screenshot(ctx context.Context, region string) (png []byte, width, height int, err error)
}

// envOS is the sandbox OS. A VM/device sandbox declares its real OS via
// WORKER_OS (the worker binary runs on the host OS, e.g. a Linux container
// hosting an Android emulator); otherwise it is the host GOOS.
func envOS() string {
	if v := strings.TrimSpace(os.Getenv("WORKER_OS")); v != "" {
		return strings.ToLower(v)
	}
	return runtime.GOOS
}

// Resolve picks the driver for this worker's platform. It never fails: an
// unsupported platform still resolves to the native driver, whose methods return
// a clear error if the daemon is unreachable.
//
// It takes no runner: every GPU/a11y operation goes through the native daemon now,
// so the platform is the only input. (It used to take a *script.Runner that it
// never read.)
func Resolve() Driver {
	return newNative(nativePlatform(), nativeDisplay())
}

// nativePlatform maps the sandbox OS to the SPEC.md platform string.
func nativePlatform() string {
	switch envOS() {
	case "android":
		return "android"
	case "windows":
		return "windows"
	case "darwin", "macos":
		return "macos"
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "linux-wayland"
		}
		return "linux-x11"
	}
}

// nativeDisplay maps the sandbox OS to the SPEC.md display string.
func nativeDisplay() string {
	switch envOS() {
	case "android":
		return "android"
	case "windows":
		return "windows"
	case "darwin", "macos":
		return "darwin"
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "wayland"
		}
		return "x11"
	}
}

// NOTE: the shell-command helpers (run/runChecked/hasTool/shq) lived here. They
// were how this package probed for tools by running `command -v` in the shell —
// but no driver calls them any more: every a11y action goes through the native
// daemon, and "is this tool present" is answered by the engine's interpreter probe
// (script.Runner.Languages) rather than by shelling out. They were dead code that
// would have broken anyway once lang became required, so they are gone.
