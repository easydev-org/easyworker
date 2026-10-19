package internal

import (
	"os"
	"os/exec"
	"strings"
	"sync"

	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
)

// CapabilitiesProbe is probeCapabilities for callers outside this package
// (main logs the git answer once at boot).
func CapabilitiesProbe() *workerv1.Capabilities { return probeCapabilities() }

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

	// git: a job that needs a repo runs `git clone`. Report whether a USABLE
	// git is on PATH — probed by RUNNING it, because a shim/stub that is on
	// PATH but cannot execute (wrong libc, missing loader) would otherwise be
	// reported as available and fail later inside the job.
	c.Git, c.GitVersion = gitProbe()

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

// gitProbe runs `git --version` once and caches the answer.
//
// The probe is a real exec, not LookPath: Android guests have no git at all,
// and a binary copied from a glibc host would be on PATH yet unrunnable under
// bionic. Both cases must report git=false so a caller knows to skip the repo
// step instead of failing halfway through a job.
var (
	gitMu      sync.Mutex
	gitDone    bool
	gitOK      bool
	gitVersion string
)

func gitProbe() (bool, string) {
	gitMu.Lock()
	defer gitMu.Unlock()
	if gitDone {
		return gitOK, gitVersion
	}
	gitDone = true
	if _, err := exec.LookPath("git"); err != nil {
		return false, ""
	}
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return false, "" // present but not runnable
	}
	// "git version 2.55.0.windows.5" -> "2.55.0.windows.5"
	s := strings.TrimSpace(string(out))
	if i := strings.Index(s, "version "); i >= 0 {
		s = s[i+len("version "):]
	}
	if s == "" {
		return false, ""
	}
	gitOK, gitVersion = true, s
	return gitOK, gitVersion
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
