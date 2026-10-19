package internal

import (
	"os"
	"runtime"
	"strings"
)

// goos is the SANDBOX OS. A device/VM sandbox (Android emulator)
// declares its real OS via WORKER_OS, because the worker binary itself runs on
// the host OS (a Linux container hosting an Android emulator, a macOS guest
// Empty = the host GOOS.
func envOS() string {
	if v := strings.TrimSpace(os.Getenv("WORKER_OS")); v != "" {
		return strings.ToLower(v)
	}
	return runtime.GOOS
}

// EnvOS returns the SANDBOX OS (the WORKER_OS override, else the host GOOS).
// Exported for cmd/easyworker: host-OS-only setup (the embedded xa11y CLI)
// is skipped on a device sandbox (android) whose computer-use driver is adb,
// not xa11y.
func EnvOS() string { return envOS() }

func goos() string   { return envOS() }
func goarch() string { return runtime.GOARCH }

// distroID reads /etc/os-release's ID (e.g. "debian", "alpine", "ubuntu"),
// lower-cased. "" when the file is absent or has no ID (e.g. windows/macos,
// or scratch images).
func distroID() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			return strings.ToLower(strings.Trim(v, `"`))
		}
	}
	return ""
}

// homeDir is the worker user's home (where `~` resolves), or "" when the
// platform has none.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
