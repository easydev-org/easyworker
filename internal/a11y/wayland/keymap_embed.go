//go:build linux
package wayland

import _ "embed"

// DefaultKeymap is a standard xkb keymap (evdev/us) used when the caller does
// not supply one. Embedded so the binary needs no system keymap at runtime.
//
//go:embed keymap.xkb
var DefaultKeymap string
