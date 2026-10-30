// Package computer is the worker-native GUI-automation layer. It replaces the
// client-side command assembly that used to live in the workspace extension:
// the worker knows its own platform and picks a Driver, so the client stays
// platform-agnostic (see the ComputerService RPC).
package computer

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/easydev-org/easyworker/internal/a11y/native"
	"github.com/easydev-org/easyworker/internal/script"
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
// unsupported platform resolves to the X11/AT-SPI driver, whose methods return
// a clear error if the tool is missing.
func Resolve(r *script.Runner) Driver {
	// native-a11y (abc-agent/worker-native): when the daemon is enabled, it owns
	// the a11y tree / input / capture, so drive it over HTTP.
	if native.Enabled() {
		return newNative(nativePlatform(), nativeDisplay())
	}
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

// ---- shared command helpers (used by every driver) ----

// cmdResult is a finished command's outcome, including what it printed. script.
// Result deliberately carries only the exit code and the language: output goes to
// the writers you pass. GUI helpers want the text, so they collect it here.
type cmdResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// run interprets one command through the engine and captures its output (not
// streamed): GUI ops are short and the caller needs the text.
func run(ctx context.Context, r *script.Runner, command string) (cmdResult, error) {
	var out, errb strings.Builder
	res, err := r.Run(ctx, script.Spec{Code: command, Stdout: &out, Stderr: &errb})
	return cmdResult{ExitCode: res.ExitCode, Stdout: out.String(), Stderr: errb.String()}, err
}

// runChecked runs a command and returns its stdout, erroring on a non-zero exit
// (with stderr in the message).
func runChecked(ctx context.Context, r *script.Runner, command string) (string, error) {
	res, err := run(ctx, r, command)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("command failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// hasTool reports whether a binary is on PATH.
func hasTool(ctx context.Context, r *script.Runner, name string) bool {
	res, err := run(ctx, r, "command -v "+shq(name))
	return err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != ""
}

// shq single-quotes a string for the builtin shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
