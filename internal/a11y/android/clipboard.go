package android

import (
	"os/exec"
	"strings"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// Clipboard uses `cmd clipboard` (Android 10+) via the platform's own shell.
// The worker runs INSIDE the Android guest, so it can call `cmd`/`service`
// directly (no adb round-trip).
type Clipboard struct{}

// NewClipboard returns the Android clipboard backend.
func NewClipboard() goa11y.Clipboard { return &Clipboard{} }

func (c *Clipboard) Name() string { return "android-clipboard" }

// Get reads the clipboard text via `cmd clipboard get` (Android 12+) or the
// legacy `service call clipboard`.
func (c *Clipboard) Get() (string, error) {
	if out, err := exec.Command("cmd", "clipboard", "get").Output(); err == nil {
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", goa11y.Unsupported("clipboard get (needs Android 12+ cmd clipboard)")
}

// Set writes the clipboard text via `cmd clipboard set`.
func (c *Clipboard) Set(text string) error {
	if err := exec.Command("cmd", "clipboard", "set", text).Run(); err != nil {
		return goa11y.Unsupported("clipboard set (needs Android 12+ cmd clipboard)")
	}
	return nil
}

// SystemKey injects device keys via `input keyevent` (HOME/BACK/APP_SWITCH/...).
func (i *Input) SystemKey(name string) error {
	switch name {
	case goa11y.SysHome:
		return runInput("keyevent", "KEYCODE_HOME")
	case goa11y.SysBack:
		return runInput("keyevent", "KEYCODE_BACK")
	case goa11y.SysRecents:
		return runInput("keyevent", "KEYCODE_APP_SWITCH")
	case goa11y.SysMenu:
		return runInput("keyevent", "KEYCODE_MENU")
	case goa11y.SysNotifications:
		return runInput("keyevent", "KEYCODE_NOTIFICATION")
	case goa11y.SysVolumeUp:
		return runInput("keyevent", "KEYCODE_VOLUME_UP")
	case goa11y.SysVolumeDown:
		return runInput("keyevent", "KEYCODE_VOLUME_DOWN")
	case goa11y.SysVolumeMute:
		return runInput("keyevent", "KEYCODE_VOLUME_MUTE")
	case goa11y.SysPower:
		return runInput("keyevent", "KEYCODE_POWER")
	}
	return goa11y.Unsupported("system key " + name + " on Android")
}
