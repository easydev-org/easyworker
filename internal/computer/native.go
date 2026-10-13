package computer

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/easydev-org/easyworker/internal/a11y/native"
)

// nativeDriver drives the native-a11y daemon over the est protocol. The daemon
// owns the a11y tree, input and capture; the worker only forwards.
type nativeDriver struct {
	platform string
	display  string
	http     *native.HTTPClient
}

func newNative(platform, display string) *nativeDriver {
	return &nativeDriver{platform: platform, display: display, http: native.NewHTTPClient()}
}

func (d *nativeDriver) Platform() string { return d.platform }
func (d *nativeDriver) Display() string  { return d.display }
func (d *nativeDriver) Tools() []string  { return []string{"native-a11y"} }

func (d *nativeDriver) Apps(ctx context.Context) ([]App, error) {
	body, err := d.http.Apps(ctx)
	if err != nil {
		return nil, err
	}
	return parseAppsText(body), nil
}

func (d *nativeDriver) Snapshot(ctx context.Context, app string, depth int) (string, error) {
	// The daemon's /tree takes a pid; the API passes an app name. When a name is
	// given, resolve it to a pid first (0 = focused).
	pid := uint32(0)
	if app != "" {
		if apps, err := d.Apps(ctx); err == nil {
			for _, a := range apps {
				if a.Name == app {
					if p, ok := atou32(a.PID); ok {
						pid = p
					}
					break
				}
			}
		}
	}
	return d.http.Tree(ctx, pid, depth)
}

func (d *nativeDriver) Find(ctx context.Context, selector, app, output string) (string, error) {
	return d.http.Find(ctx, selector, 0, output)
}

func (d *nativeDriver) Action(ctx context.Context, action, target, value, app string) error {
	return d.http.Action(ctx, action, target, value)
}

func (d *nativeDriver) Click(ctx context.Context, x, y int, button string, count int) error {
	// Input is the est stream's job (low latency, same connection as video).
	c, err := native.Shared(ctx)
	if err != nil {
		return err
	}
	btn := 1
	switch button {
	case "right":
		btn = 3
	case "middle":
		btn = 2
	}
	if err := c.Move(int32(x), int32(y)); err != nil {
		return err
	}
	if count < 1 {
		count = 1
	}
	for i := 0; i < count; i++ {
		if err := c.Button(btn, true); err != nil {
			return err
		}
		if err := c.Button(btn, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *nativeDriver) Type(ctx context.Context, text, target string) error {
	c, err := native.Shared(ctx)
	if err != nil {
		return err
	}
	return c.Text(text)
}

func (d *nativeDriver) Key(ctx context.Context, key string, held []string) error {
	c, err := native.Shared(ctx)
	if err != nil {
		return err
	}
	return c.Key(key, 0, true)
}

func (d *nativeDriver) Scroll(ctx context.Context, x, y, dx, dy int) error {
	c, err := native.Shared(ctx)
	if err != nil {
		return err
	}
	return c.Scroll(int32(x), int32(y), int32(dx), int32(dy))
}

func (d *nativeDriver) Drag(ctx context.Context, fromX, fromY, toX, toY, durationMs int) error {
	c, err := native.Shared(ctx)
	if err != nil {
		return err
	}
	if err := c.Move(int32(fromX), int32(fromY)); err != nil {
		return err
	}
	if err := c.Button(1, true); err != nil {
		return err
	}
	if err := c.Move(int32(toX), int32(toY)); err != nil {
		return err
	}
	return c.Button(1, false)
}

func (d *nativeDriver) Screenshot(ctx context.Context, region string) ([]byte, int, int, error) {
	// Discrete one-shot → HTTP (concurrent, like the other a11y queries).
	png, err := d.http.Screenshot(ctx, region)
	if err != nil {
		return nil, 0, 0, err
	}
	w, h := pngSize(png)
	return png, w, h, nil
}

// parseAppsText decodes the daemon's /apps body, one app per line:
//
//	pid \t name \t focused
//
// (matching the goa11y CLI / xa11y output).
func parseAppsText(body string) []App {
	var apps []App
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		apps = append(apps, App{
			PID:     cols[0],
			Name:    cols[1],
			Focused: len(cols) > 2 && cols[2] == "focused",
		})
	}
	return apps
}

// atou32 parses a decimal pid string.
func atou32(s string) (uint32, bool) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// pngSize reads width/height from a PNG IHDR (bytes 16..24, big-endian). Returns
// 0,0 for a non-PNG body (the caller may still forward it).
func pngSize(b []byte) (int, int) {
	if len(b) < 24 || b[0] != 0x89 || b[1] != 'P' || b[2] != 'N' || b[3] != 'G' {
		return 0, 0
	}
	w := int(binary.BigEndian.Uint32(b[16:20]))
	h := int(binary.BigEndian.Uint32(b[20:24]))
	return w, h
}
