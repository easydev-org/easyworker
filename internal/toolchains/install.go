package toolchains

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ulikunitz/xz"
)

const (
	// DefaultIndexURL points at the in-cluster artifact service. It is only a
	// DEFAULT: WORKER_TOOLCHAIN_INDEX always wins (the installer hard-codes no
	// mirror, and the image hard-codes no index address).
	DefaultIndexURL = "http://artifact.worker.svc.cluster.local/artifacts/generic/toolchains/index/index.json"
	// DefaultRoot is where versions are installed.
	DefaultRoot = "/opt/toolchains"

	markerName = ".installed"
)

// Spec is one requested toolchain (name + pinned version).
type Spec struct {
	Name    string
	Version string
}

// Installer installs toolchains from a published index.
type Installer struct {
	root   string
	index  string
	client *http.Client
	logf   func(string, ...any)

	mu    sync.Mutex // serializes Ensure within this process
	idxMu sync.Mutex
	idx   *Index
}

// New builds an Installer. root/index default to DefaultRoot/DefaultIndexURL;
// client defaults to http.DefaultClient; logf may be nil.
func New(root, indexURL string, client *http.Client, logf func(string, ...any)) *Installer {
	if root == "" {
		root = DefaultRoot
	}
	if indexURL == "" {
		indexURL = DefaultIndexURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Installer{root: root, index: indexURL, client: client, logf: logf}
}

// Root is the install root (exposed for tests / PATH hints).
func (in *Installer) Root() string { return in.root }

func (in *Installer) loadIndex(ctx context.Context) (*Index, error) {
	in.idxMu.Lock()
	defer in.idxMu.Unlock()
	if in.idx != nil {
		return in.idx, nil
	}
	idx, err := LoadIndex(ctx, in.client, in.index)
	if err != nil {
		return nil, err
	}
	in.idx = idx
	return idx, nil
}

// Ensure installs every spec (and, transitively, each `requires`) idempotently
// and returns the bin dirs to add to PATH. Any failure aborts with an error —
// it never leaves a half-installed version visible.
func (in *Installer) Ensure(ctx context.Context, specs []Spec) ([]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	idx, err := in.loadIndex(ctx)
	if err != nil {
		return nil, err
	}

	var bins []string
	seen := map[string]bool{}
	var visit func(Spec) error
	visit = func(sp Spec) error {
		key := sp.Name + "@" + sp.Version
		if seen[key] {
			return nil
		}
		tc, ok := idx.Toolchains[sp.Name]
		if !ok {
			return fmt.Errorf("toolchain %q not in index", sp.Name)
		}
		if !tc.allowsOS(runtime.GOOS) {
			return fmt.Errorf("toolchain %q is not installable on %s (allowed: %v); build it on a %s sandbox instead",
				sp.Name, runtime.GOOS, tc.OS, tc.OS[0])
		}
		v, ok := tc.Versions[sp.Version]
		if !ok {
			return fmt.Errorf("toolchain %q has no version %q in index", sp.Name, sp.Version)
		}
		for _, req := range tc.Requires {
			rtc, ok := idx.Toolchains[req]
			if !ok {
				return fmt.Errorf("toolchain %q requires %q, which is not in index", sp.Name, req)
			}
			rv, err := greatestVersion(rtc.Versions)
			if err != nil {
				return fmt.Errorf("resolve %q for %q: %w", req, sp.Name, err)
			}
			if err := visit(Spec{Name: req, Version: rv}); err != nil {
				return err
			}
		}
		seen[key] = true
		b, err := in.installVersion(ctx, sp.Name, sp.Version, v)
		if err != nil {
			return err
		}
		bins = append(bins, b...)
		return nil
	}
	for _, sp := range specs {
		if err := visit(sp); err != nil {
			return nil, err
		}
	}
	return dedupe(bins), nil
}

func (in *Installer) installVersion(ctx context.Context, name, ver string, v Version) ([]string, error) {
	dir := filepath.Join(in.root, name, ver)
	if hasMarker(dir) {
		in.logf("toolchains: %s@%s already installed", name, ver)
		applyEnv(dir, v.Env)
		return binDirs(dir, v), nil
	}
	if err := os.MkdirAll(filepath.Join(in.root, name), 0o755); err != nil {
		return nil, err
	}

	unlock, err := lockRoot(in.root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if hasMarker(dir) { // another process finished while we waited
		applyEnv(dir, v.Env)
		return binDirs(dir, v), nil
	}

	// install_prefix marks an INSTALLER that bakes its output path into the
	// installed files (Miniconda rewrites every script's shebang to the prefix).
	// Such a toolchain cannot be unpacked to `.tmp` and renamed — install it IN
	// PLACE (accepting that a failed install leaves a partial dir, which is then
	// cleaned up). Everything else uses the atomic tmp+rename path.
	inPlace := v.InstallPrefix != ""
	work := dir + ".tmp"
	if inPlace {
		work = dir
	}
	cleanup := func() {
		if !inPlace {
			_ = os.RemoveAll(work)
		}
	}
	if err := os.RemoveAll(work); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	// unpack_dir: unpack into a subdir (installers that refuse their own dir).
	unpackDest := work
	if v.UnpackDir != "" {
		unpackDest = filepath.Join(work, v.UnpackDir)
		if err := os.MkdirAll(unpackDest, 0o755); err != nil {
			cleanup()
			return nil, err
		}
	}
	arts := platformArtifacts(v.Artifacts, runtime.GOOS, runtime.GOARCH)
	if len(arts) == 0 {
		cleanup()
		return nil, fmt.Errorf("%s@%s: no artifact for %s/%s", name, ver, runtime.GOOS, runtime.GOARCH)
	}
	for _, art := range arts {
		if err := in.fetchArtifact(ctx, art, unpackDest); err != nil {
			cleanup()
			return nil, fmt.Errorf("%s@%s: %w", name, ver, err)
		}
		if art.Rename != "" {
			if err := applyRename(unpackDest, art.Rename); err != nil {
				cleanup()
				return nil, fmt.Errorf("%s@%s: %w", name, ver, err)
			}
		}
	}
	if len(v.Install) > 0 {
		// {root} expands to the INSTALL prefix (version root, or
		// <root>/<install_prefix>); cwd is the unpack dir.
		installRoot := installBase(work, v.InstallPrefix)
		if err := runInstall(v.Install, installRoot, unpackDest, in.logf); err != nil {
			cleanup()
			return nil, fmt.Errorf("%s@%s install: %w", name, ver, err)
		}
	}
	if !inPlace {
		if err := os.RemoveAll(dir); err != nil {
			cleanup()
			return nil, err
		}
		if err := os.Rename(work, dir); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte("ok\n"), 0o644); err != nil {
		return nil, err
	}
	// Runtime env (e.g. ruby's LD_LIBRARY_PATH): merge into the worker's own
	// process env so subsequent jobs (which inherit it) see it. Re-applied on
	// every install, so it survives an already-installed (marker) fast path too.
	applyEnv(dir, v.Env)
	in.logf("toolchains: installed %s@%s -> %s", name, ver, dir)
	bins := binDirs(dir, v)
	ensureExecutable(bins)
	return bins, nil
}

// ensureExecutable chmods 0755 the regular files directly inside each bin dir.
// Some upstream tarballs ship their launcher without the exec bit (e.g.
// App::cpanminus' `cpanm`); making bin entries runnable is the installer's job.
func ensureExecutable(dirs []string) {
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type().IsRegular() {
				_ = os.Chmod(filepath.Join(d, e.Name()), 0o755)
			}
		}
	}
}

// applyEnv sets each NAME=VALUE in the process env, expanding {root}. It is
// called on both the fresh-install and already-installed paths (so a restarted
// worker that finds the marker still exports the env). PATH-style variables
// (LD_LIBRARY_PATH) are MERGED, not replaced, so two toolchains contributing to
// the same variable (ruby + clang) do not clobber each other.
func applyEnv(root string, env map[string]string) {
	for name, val := range env {
		if name == "" {
			continue
		}
		val = expandRoot(val, root)
		if name == "LD_LIBRARY_PATH" {
			_ = os.Setenv(name, MergePath(os.Getenv(name), []string{val}))
			continue
		}
		_ = os.Setenv(name, val)
	}
}

// installBase resolves the install prefix: the version root, or
// <version-root>/<prefix> when InstallPrefix is set.
func installBase(root, prefix string) string {
	if prefix == "" {
		return root
	}
	return filepath.Join(root, prefix)
}

// fetchArtifact downloads one file, verifies its sha256, and unpacks it into
// dest. The raw bytes are spooled to a temp file so the hash covers the whole
// file (a streaming tar reader can stop before the archive's trailing bytes).
func (in *Installer) fetchArtifact(ctx context.Context, art Artifact, dest string) error {
	format := normalizeFormat(art.Format)
	body, err := in.get(ctx, art.URL)
	if err != nil {
		return err
	}
	defer body.Close()

	spool, err := os.CreateTemp("", "tc-art-*")
	if err != nil {
		return err
	}
	spoolPath := spool.Name()
	defer os.Remove(spoolPath)
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(spool, h), body); err != nil {
		_ = spool.Close()
		return err
	}
	if err := spool.Close(); err != nil {
		return err
	}
	if got, want := hex.EncodeToString(h.Sum(nil)), strings.TrimSpace(art.SHA256); !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch for %s: got %s want %s", art.URL, got, want)
	}

	switch format {
	case "tar.gz", "tar.xz", "gz", "phar", "raw":
		f, err := os.Open(spoolPath)
		if err != nil {
			return err
		}
		defer f.Close()
		switch format {
		case "tar.gz":
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			return extractTar(gz, dest, art.Strip)
		case "tar.xz":
			xr, err := xz.NewReader(f)
			if err != nil {
				return err
			}
			return extractTar(xr, dest, art.Strip)
		case "gz":
			target, err := singleTarget(dest, art)
			if err != nil {
				return err
			}
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			return writeFileFrom(target, gz, 0o755)
		default: // phar, raw
			target, err := singleTarget(dest, art)
			if err != nil {
				return err
			}
			return writeFileFrom(target, f, 0o755)
		}
	case "zip":
		return extractZip(spoolPath, dest, art.Strip)
	default:
		return fmt.Errorf("unsupported format %q", art.Format)
	}
}

func (in *Installer) get(ctx context.Context, url string) (io.ReadCloser, error) {
	if url == "" {
		return nil, fmt.Errorf("artifact without url")
	}
	if !strings.Contains(url, "://") {
		return os.Open(url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := in.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// singleTarget resolves the destination file for a single-file format. `bin`
// names the file path relative to the version root (its parent dir joins PATH);
// when empty, the URL stem is used.
func singleTarget(dest string, art Artifact) (string, error) {
	rel := art.Bin
	if rel == "" {
		rel = fileStem(art.URL)
	}
	rel = expandRoot(rel, dest)
	if filepath.IsAbs(rel) {
		clean := filepath.Clean(rel)
		if !strings.HasPrefix(clean, filepath.Clean(dest)+string(filepath.Separator)) {
			return "", fmt.Errorf("single-file target escapes version root: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return "", err
		}
		return clean, nil
	}
	target, err := safeJoin(dest, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	return target, nil
}

// platformArtifacts filters a version's artifacts to the current platform
// (empty OS/Arch = wildcard), so ONE index serves linux AND windows/macos.
func platformArtifacts(all []Artifact, goos, goarch string) []Artifact {
	out := make([]Artifact, 0, len(all))
	for _, a := range all {
		if a.matches(goos, goarch) {
			out = append(out, a)
		}
	}
	return out
}

// binDirs computes the PATH dirs for an installed version.
func binDirs(root string, v Version) []string {
	// `bin` is relative to the INSTALL prefix (version root, or
	// <root>/<install_prefix>); unpack_dir is only the scratch location for the
	// archive + install cwd.
	base := installBase(root, v.InstallPrefix)
	var out []string
	add := func(p string) {
		if p != "" {
			out = append(out, p)
		}
	}
	arts := platformArtifacts(v.Artifacts, runtime.GOOS, runtime.GOARCH)
	if len(arts) == 0 {
		return []string{base}
	}
	for _, a := range arts {
		rel := expandRoot(a.Bin, base)
		switch normalizeFormat(a.Format) {
		case "gz", "phar", "raw":
			p := rel
			if p == "" {
				p = filepath.Join(base, fileStem(a.URL))
			} else if !filepath.IsAbs(p) {
				p = filepath.Join(base, p)
			}
			add(filepath.Dir(p))
		default:
			if rel == "" {
				add(base)
			} else if filepath.IsAbs(rel) {
				add(filepath.Clean(rel))
			} else {
				add(filepath.Join(base, rel))
			}
		}
	}
	return out
}

func runInstall(argv []string, root, cwd string, logf func(string, ...any)) error {
	if len(argv) == 0 {
		return nil
	}
	args := make([]string, len(argv))
	for i, a := range argv {
		args[i] = expandRoot(a, root)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = cwd
	// Installer output goes to stderr: the worker prints its one-time
	// enrollment code to stdout, which must stay clean.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	logf("toolchains: run %v (cwd %s)", args, cwd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %w", args, err)
	}
	return nil
}

func hasMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, markerName))
	return err == nil
}

// normalizeFormat lower-cases and validates an artifact format.
func normalizeFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "tar.gz", "tgz":
		return "tar.gz"
	case "tar.xz", "txz":
		return "tar.xz"
	case "zip":
		return "zip"
	case "gz":
		return "gz"
	case "phar":
		return "phar"
	case "raw":
		return "raw"
	default:
		return ""
	}
}

// greatestVersion picks the highest version key (used to resolve `requires`,
// which carry no explicit version).
func greatestVersion(versions map[string]Version) (string, error) {
	if len(versions) == 0 {
		return "", fmt.Errorf("no versions")
	}
	keys := make([]string, 0, len(versions))
	for k := range versions {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return versionLess(keys[i], keys[j]) })
	return keys[len(keys)-1], nil
}

// versionLess compares dotted versions numerically where possible, falling back
// to a string compare for non-numeric components.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi string
		if i < len(as) {
			ai = as[i]
		}
		if i < len(bs) {
			bi = bs[i]
		}
		an, aerr := strconv.Atoi(ai)
		bn, berr := strconv.Atoi(bi)
		if aerr == nil && berr == nil {
			if an != bn {
				return an < bn
			}
			continue
		}
		if ai != bi {
			return ai < bi
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// MergePath prepends dirs to a PATH-style string (dedup, existing order kept).
// Used for both the worker's own process env and the image's PATH hints.
func MergePath(cur string, dirs []string) string {
	parts := filepath.SplitList(cur)
	have := map[string]bool{}
	for _, p := range parts {
		have[p] = true
	}
	var prepend []string
	for _, d := range dirs {
		if d == "" || have[d] {
			continue
		}
		have[d] = true
		prepend = append(prepend, d)
	}
	if len(prepend) == 0 {
		return cur
	}
	return strings.Join(append(prepend, parts...), string(os.PathListSeparator))
}

// ExistingBinDirs lists the bin dirs of toolchains already installed under
// root (root/<lang>/<ver>/bin), so a restarted worker can re-add them to PATH
// without reinstalling.
func ExistingBinDirs(root string) []string {
	if root == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*", "bin"))
	sort.Strings(matches)
	var out []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			out = append(out, m)
		}
	}
	return out
}

// ParseSpecs parses "name=version" entries separated by commas and/or
// newlines; blank lines and `#` comments are ignored.
func ParseSpecs(s string) ([]Spec, error) {
	var out []Spec
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, f := range strings.Split(line, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			name, ver, ok := strings.Cut(f, "=")
			name, ver = strings.TrimSpace(name), strings.TrimSpace(ver)
			if !ok || name == "" || ver == "" {
				return nil, fmt.Errorf("toolchain spec %q must be name=version", f)
			}
			out = append(out, Spec{Name: name, Version: ver})
		}
	}
	return out, nil
}

// SpecsFromEnv merges the WORKSPACE_TOOLCHAINS env value with a workspace
// `.toolchains` file (the file is appended, so the env can extend it).
func SpecsFromEnv(env, workspace string) ([]Spec, error) {
	raw := env
	if workspace != "" {
		if b, err := os.ReadFile(filepath.Join(workspace, ".toolchains")); err == nil {
			if strings.TrimSpace(raw) != "" {
				raw += "\n"
			}
			raw += string(b)
		}
	}
	return ParseSpecs(raw)
}
