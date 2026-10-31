package computer

import (
	"runtime"
	"testing"
)

func TestResolvePlatform(t *testing.T) {
	// THE EMPTY WORKER_OS CASES DEPEND ON WHERE THIS RUNS. Resolve() with no override reports the host:
	// linux-x11 or linux-wayland on Linux, macos on Darwin, windows on Windows. The original table
	// expected linux unconditionally, so these two cases failed the first time the suite ran on macOS —
	// a test that assumed its own platform rather than asking for it.
	//
	// The overridden cases are absolute: an explicit WORKER_OS is a statement about the host and must
	// win everywhere. Those keep their fixed expectations.
	hostDefault := "linux-x11"
	// Only Linux reads WAYLAND_DISPLAY; elsewhere the answer is the host.
	waylandDefault := hostDefault
	if runtime.GOOS == "linux" {
		waylandDefault = "linux-wayland"
	}
	switch runtime.GOOS {
	case "darwin":
		hostDefault, waylandDefault = "macos", "macos"
	case "windows":
		hostDefault, waylandDefault = "windows", "windows"
	}
	cases := []struct {
		workerOS, wayland, want string
	}{
		{"android", "", "android"},
		{"windows", "", "windows"},
		{"darwin", "", "macos"},
		// No override: this host, whatever it is.
		{"", "", hostDefault},
		// No override but Wayland is set. ONLY LINUX READS IT: nativePlatform consults WAYLAND_DISPLAY in
		// its linux branch alone, so on macOS and Windows the answer is still the host itself.
		{"", "wayland-0", waylandDefault},
	}
	for _, c := range cases {
		t.Setenv("WORKER_OS", c.workerOS)
		t.Setenv("WAYLAND_DISPLAY", c.wayland)
		d := Resolve()
		if got := d.Platform(); got != c.want {
			t.Errorf("WORKER_OS=%q WAYLAND=%q: platform=%q want %q", c.workerOS, c.wayland, got, c.want)
		}
	}
}

func TestGrimRegion(t *testing.T) {
	cases := map[string]string{
		"10,20,300,200":   "300x200+10+20",
		"0, 0, 1280, 720": "1280x720+0+0",
		"300x200+10+20":   "300x200+10+20", // already grim form -> unchanged
		"nonsense":        "nonsense",
	}
	for in, want := range cases {
		if got := grimRegion(in); got != want {
			t.Errorf("grimRegion(%q) = %q, want %q", in, got, want)
		}
	}
}
