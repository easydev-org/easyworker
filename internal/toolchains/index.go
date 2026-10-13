// Package toolchains implements the on-demand language toolchain installer:
// it reads the published index (see abc-protocol/deploy/DEVELOP.md "Toolchain
// index"), downloads + verifies + unpacks each declared toolchain into
// $WORKER_TOOLCHAIN_ROOT, and returns the bin dirs to add to PATH.
//
// Nothing here hard-codes a mirror: the index URL is an env knob
// (WORKER_TOOLCHAIN_INDEX) whose default points at the in-cluster artifact
// service but is always overridable. The index's `url` fields are DATA.
package toolchains

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Index is the published toolchain index (schema 1).
type Index struct {
	Schema     int                  `json:"schema"`
	Toolchains map[string]Toolchain `json:"toolchains"`
}

// Toolchain is one language: its versions and the other toolchains it needs.
type Toolchain struct {
	Requires []string           `json:"requires"`
	Versions map[string]Version `json:"versions"`
	// OS, when non-empty, lists the platforms this toolchain is installable on
	// ("linux"/"windows"/"darwin"). Empty = any. It encodes intent that is NOT
	// visible from the artifacts alone: e.g. `flutter` is only meaningful on a
	// macOS/Windows host when it must produce a macOS/Windows desktop build, so
	// the index can restrict it to those, while go/node/... stay linux-only (a
	// linux sandbox builds them; there is no reason to install them on a VM).
	OS []string `json:"os,omitempty"`
	// Toolchains that are ALWAYS fine to install anywhere (no host-specific
	// build output) leave OS empty.
}

// allowsOS reports whether this toolchain may be installed on goos.
func (tc Toolchain) allowsOS(goos string) bool {
	if len(tc.OS) == 0 {
		return true
	}
	for _, o := range tc.OS {
		if o == goos {
			return true
		}
	}
	return false
}

// Version is one pinned toolchain version.
type Version struct {
	Artifacts []Artifact `json:"artifacts"`
	Install   []string   `json:"install"`
	// Env is runtime environment applied after a successful install (e.g.
	// ruby's LD_LIBRARY_PATH). Values may contain the "{root}" placeholder.
	Env map[string]string `json:"env"`
	// UnpackDir, when set, unpacks every artifact into <version-root>/<UnpackDir>
	// (and runs install[] there). Needed when the installer refuses its own
	// directory (rust/clojure install.sh).
	UnpackDir string `json:"unpack_dir"`
	// InstallPrefix, when set, is the subdir of the version root that install[]
	// and `bin` are resolved against (the installer's OUTPUT dir). E.g. conda:
	// the installer writes to <root>/miniconda, so bin resolves under it.
	InstallPrefix string `json:"install_prefix"`
}

// Artifact is one downloadable file of a version.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Format string `json:"format"`
	Strip  int    `json:"strip"`
	Bin    string `json:"bin"`
	// Rename renames an unpacked top-level entry after `strip`, "src->dst"
	// (both relative to the version root). Used e.g. by dart
	// ("dart-sdk->dart") so `bin` can name the final layout.
	Rename string `json:"rename"`
	// OS/Arch restrict this artifact to a platform ("linux"/"windows"/"darwin",
	// "amd64"/"arm64"). Empty = any. A version may list one artifact per
	// platform; the installer picks the ones matching its own OS/Arch, so a
	// single index serves linux AND windows/macos sandboxes.
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
}

// matches reports whether the artifact applies to the given platform. An empty
// OS/Arch is a wildcard.
func (a Artifact) matches(goos, goarch string) bool {
	if a.OS != "" && a.OS != goos {
		return false
	}
	if a.Arch != "" && a.Arch != goarch {
		return false
	}
	return true
}

// ParseIndex decodes and validates an index. Validation is strict: a missing
// sha256 or an unknown format is rejected up front (never a half install).
func ParseIndex(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if idx.Schema != 1 {
		return nil, fmt.Errorf("unsupported index schema %d (want 1)", idx.Schema)
	}
	for name, tc := range idx.Toolchains {
		if len(tc.Versions) == 0 {
			return nil, fmt.Errorf("toolchain %q has no versions", name)
		}
		for ver, v := range tc.Versions {
			if len(v.Artifacts) == 0 {
				return nil, fmt.Errorf("%s@%s has no artifacts", name, ver)
			}
			for _, a := range v.Artifacts {
				switch {
				case a.URL == "":
					return nil, fmt.Errorf("%s@%s: artifact without url", name, ver)
				case a.SHA256 == "":
					return nil, fmt.Errorf("%s@%s: artifact %s without sha256", name, ver, a.URL)
				case normalizeFormat(a.Format) == "":
					return nil, fmt.Errorf("%s@%s: artifact %s has unknown format %q", name, ver, a.URL, a.Format)
				}
			}
		}
	}
	return &idx, nil
}

// LoadIndex fetches and parses an index from an http(s) URL, or from a local
// path when src has no scheme (handy for tests and offline runs).
func LoadIndex(ctx context.Context, client *http.Client, src string) (*Index, error) {
	var b []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if rerr != nil {
			return nil, rerr
		}
		resp, derr := client.Do(req)
		if derr != nil {
			return nil, derr
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s", src, resp.Status)
		}
		if b, err = io.ReadAll(resp.Body); err != nil {
			return nil, err
		}
	} else {
		if b, err = os.ReadFile(src); err != nil {
			return nil, err
		}
	}
	return ParseIndex(b)
}
