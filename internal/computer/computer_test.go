package computer

import (
	"testing"
)

func TestResolvePlatform(t *testing.T) {
	cases := []struct {
		workerOS, wayland, want string
	}{
		{"android", "", "android"},
		{"windows", "", "windows"},
		{"darwin", "", "macos"},
		{"", "", "linux-x11"},
		{"", "wayland-0", "linux-wayland"},
	}
	for _, c := range cases {
		t.Setenv("WORKER_OS", c.workerOS)
		t.Setenv("WAYLAND_DISPLAY", c.wayland)
		d := Resolve(nil)
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
