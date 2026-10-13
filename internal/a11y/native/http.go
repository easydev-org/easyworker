package native

// The native-a11y daemon exposes TWO channels:
//
//   · est:// (TCP, port 48079) — the low-latency stream: H.264 video and input
//     injection. Continuous, one connection, video on its own thread.
//   · HTTP (port 48080) — discrete queries: the a11y tree (/apps, /tree,
//     /find), actions, and screenshots. Each request is independent, so many
//     can run concurrently — which est's single control thread cannot do.
//
// So the worker splits them: video/input go over est (stream.go), a11y and
// screenshots go over HTTP (this file). Same command surface as the goa11y
// CLI, so it matches the daemon's other platforms.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HTTPAddr is the a11y HTTP endpoint of the daemon (EST.md's sibling).
func HTTPAddr() string {
	if a := os.Getenv("NATIVE_A11Y_HTTP"); a != "" {
		return a
	}
	return "127.0.0.1:48080"
}

// HTTPClient talks to the daemon's HTTP a11y endpoint.
type HTTPClient struct {
	base string
	hc   *http.Client
}

// NewHTTPClient builds a client for the daemon's HTTP endpoint.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		base: "http://" + HTTPAddr(),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

// get performs a GET and returns the body (the daemon replies text/plain).
func (c *HTTPClient) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("native-a11y http %s: %s", path, strings.TrimSpace(string(body)))
	}
	// The daemon reports failures in the body (200 + "error: ...").
	if s := strings.TrimSpace(string(body)); strings.HasPrefix(s, "error:") {
		return nil, fmt.Errorf("native-a11y: %s", strings.TrimPrefix(s, "error:"))
	}
	return body, nil
}

// Apps returns the raw /apps body (tab-separated: pid\tname\tfocused).
func (c *HTTPClient) Apps(ctx context.Context) (string, error) {
	b, err := c.get(ctx, "/apps", nil)
	return string(b), err
}

// Tree returns the a11y tree for pid (0 = focused) to depth.
func (c *HTTPClient) Tree(ctx context.Context, pid uint32, depth int) (string, error) {
	q := url.Values{}
	if pid != 0 {
		q.Set("pid", fmt.Sprintf("%d", pid))
	}
	if depth > 0 {
		q.Set("depth", fmt.Sprintf("%d", depth))
	}
	b, err := c.get(ctx, "/tree", q)
	return string(b), err
}

// Find runs a selector; output is pretty | bounds | center.
func (c *HTTPClient) Find(ctx context.Context, selector string, pid uint32, output string) (string, error) {
	q := url.Values{"sel": {selector}}
	if output != "" {
		q.Set("o", output)
	}
	if pid != 0 {
		q.Set("pid", fmt.Sprintf("%d", pid))
	}
	b, err := c.get(ctx, "/find", q)
	return string(b), err
}

// Action performs an a11y action on a target selector.
func (c *HTTPClient) Action(ctx context.Context, action, target, value string) error {
	q := url.Values{"act": {action}, "sel": {target}}
	if value != "" {
		q.Set("value", value)
	}
	_, err := c.get(ctx, "/action", q)
	return err
}

// Screenshot fetches a PNG (region is "" or "x,y,w,h").
func (c *HTTPClient) Screenshot(ctx context.Context, region string) ([]byte, error) {
	q := url.Values{}
	if region != "" {
		q.Set("region", region)
	}
	return c.get(ctx, "/screenshot", q)
}

// Health reports whether the daemon's HTTP endpoint is up.
func (c *HTTPClient) Health(ctx context.Context) error {
	_, err := c.get(ctx, "/health", nil)
	return err
}
