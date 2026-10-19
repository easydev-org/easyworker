// Package webui embeds the worker's built-in control panel.
//
// The panel is a Svelte 5 + Tailwind 4 SPA from the easyworker-webui repo
// (github.com/easydev-org/easyworker-webui), built into dist/ and embedded
// here, served from the worker's own port at "/", so it is SAME-ORIGIN with the
// Connect RPC: the browser needs only a bearer token, no CORS.
//
// It presents a login gate (bearer token, or one-time-code claim of an
// unclaimed worker), then a shell-style console (run commands with live
// streamed output, a client-side virtual cwd) with mutually-exclusive Files
// and Jobs drawers. It talks to worker.v1.WorkerService + worker.v1.WorkerEnroll
// directly (including the server-streaming WatchJob).
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The built SPA. dist/ is produced by the Containerfile's node stage (which
// clones easyworker-webui and runs `npm run build`), or locally with
// `OUT_DIR=… npm run build` in that repo. It is NOT committed.
//
//go:embed all:dist
var dist embed.FS

// distEmbedded reports whether the embedded SPA is a REAL build or the
// 14-byte stub some local build left behind.
//
// internal/webui/dist/ is git-ignored on purpose: the real panel comes from
// the easyworker-webui repo in the Containerfile's node stage. The catch is
// that a plain `go build` in a working tree with no dist/ fails outright
// ("pattern all:dist: no matching files"), so people create a placeholder to
// get it to compile — and then that placeholder gets EMBEDDED and shipped.
// That is exactly how v0.2.0-v0.3.0 ended up serving "<html></html>" as the
// control panel.
//
// It is logged once at startup instead of failing the build: the worker is
// still fully functional over RPC without a panel, and refusing to start would
// turn a cosmetic mistake into an outage.
func distLooksStubbed() bool {
	b, err := dist.ReadFile("dist/index.html")
	if err != nil {
		return true
	}
	return len(b) < 256
}

// PanelSize returns the byte length of the embedded index.html, so the caller
// can log whether the panel looks real. A real SPA index is ~0.5 KB and pulls
// /assets/*; the placeholder is <256 B.
func PanelSize() int {
	b, err := dist.ReadFile("dist/index.html")
	if err != nil {
		return 0
	}
	return len(b)
}

// PanelLooksStubbed reports whether the embedded panel is a placeholder.
func PanelLooksStubbed() bool { return distLooksStubbed() }

// Handler serves the panel: static assets by exact path, and index.html for
// everything else (the SPA has no client-side routes, but this keeps deep
// links harmless).
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embedded FS is compile-time; unreachable
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			// Unknown path → the SPA entry (client routing).
			data, err = fs.ReadFile(sub, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", contentType(p))
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
}

func contentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
