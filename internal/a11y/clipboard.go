package goa11y

// Clipboard reads and writes the sandbox's text clipboard. Backends shell out
// to the platform tool (xclip/wl-clipboard on Linux, pbcopy/pbpaste on macOS,
// PowerShell/clip on Windows, adb service call on Android). A backend that has
// no tool returns Unsupported.
type Clipboard interface {
	Get() (string, error)
	Set(text string) error
	Name() string
}

// ClipboardFor returns the clipboard backend for this platform, or nil when
// none is available. It is wired by the platform package (plat).
var clipboardFactory func() Clipboard

// SetClipboardFactory is called by the platform package at init.
func SetClipboardFactory(f func() Clipboard) { clipboardFactory = f }

// NewClipboard returns the platform clipboard backend, or nil.
func NewClipboard() Clipboard {
	if clipboardFactory == nil {
		return nil
	}
	return clipboardFactory()
}
