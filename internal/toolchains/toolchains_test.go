package toolchains

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGz builds a single-root tar.gz: root/<files...>.
func tarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := root + "/"
	if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		b := []byte(body)
		if err := tw.WriteHeader(&tar.Header{Name: dir + name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipSingle(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// serveIndex starts an httptest server hosting the given blobs and returns the
// index URL plus the blobs' base URL.
func serveIndex(t *testing.T, blobs map[string][]byte, index func(base string) string) (string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/blobs/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/blobs/")
		b, ok := blobs[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(index(srv.URL)))
	})
	return srv.URL + "/index.json", srv.URL
}

func TestParseIndexStrict(t *testing.T) {
	cases := map[string]string{
		"no schema":  `{"toolchains":{"go":{"versions":{"1.0":{"artifacts":[{"url":"u","sha256":"a","format":"tar.gz"}]}}}}}`,
		"no sha256":  `{"schema":1,"toolchains":{"go":{"versions":{"1.0":{"artifacts":[{"url":"u","format":"tar.gz"}]}}}}}`,
		"bad format": `{"schema":1,"toolchains":{"go":{"versions":{"1.0":{"artifacts":[{"url":"u","sha256":"a","format":"rar"}]}}}}}`,
		"no arts":    `{"schema":1,"toolchains":{"go":{"versions":{"1.0":{"artifacts":[]}}}}}`,
	}
	for name, doc := range cases {
		if _, err := ParseIndex([]byte(doc)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestEnsureTarGz(t *testing.T) {
	blob := tarGz(t, "go", map[string]string{"bin/go": "#!/bin/sh\necho go\n"})
	idxURL, _ := serveIndex(t, map[string][]byte{"go.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"go": map[string]any{"versions": map[string]any{
				"1.27.1": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/go.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1, "bin": "bin",
				}}},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})

	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	bins, err := in.Ensure(context.Background(), []Spec{{Name: "go", Version: "1.27.1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "go", "1.27.1", "bin")
	if len(bins) != 1 || bins[0] != want {
		t.Fatalf("bins=%v want [%s]", bins, want)
	}
	if _, err := os.Stat(filepath.Join(want, "go")); err != nil {
		t.Fatalf("binary not extracted: %v", err)
	}
	// Idempotent: second call returns the same dirs, no error.
	if bins2, err := in.Ensure(context.Background(), []Spec{{Name: "go", Version: "1.27.1"}}); err != nil || len(bins2) != 1 {
		t.Fatalf("second ensure: bins=%v err=%v", bins2, err)
	}
}

func TestEnsureShaMismatch(t *testing.T) {
	blob := tarGz(t, "go", map[string]string{"bin/go": "x"})
	idxURL, _ := serveIndex(t, map[string][]byte{"go.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"go": map[string]any{"versions": map[string]any{
				"1.27.1": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/go.tgz", "sha256": strings.Repeat("0", 64), "format": "tar.gz", "strip": 1, "bin": "bin",
				}}},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	if _, err := in.Ensure(context.Background(), []Spec{{Name: "go", Version: "1.27.1"}}); err == nil {
		t.Fatal("expected sha mismatch error")
	}
	// No half install: the version dir must not exist.
	if _, err := os.Stat(filepath.Join(root, "go", "1.27.1")); !os.IsNotExist(err) {
		t.Fatalf("half install left behind: %v", err)
	}
}

func TestEnsureRequires(t *testing.T) {
	jdk := tarGz(t, "jdk-25", map[string]string{"bin/java": "java"})
	kotlin := zipSingle(t, "kotlinc/bin/kotlinc", "kotlinc")
	idxURL, _ := serveIndex(t, map[string][]byte{"jdk.tgz": jdk, "kotlin.zip": kotlin}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"java25": map[string]any{"versions": map[string]any{
				"25.0.4": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/jdk.tgz", "sha256": sha(jdk), "format": "tar.gz", "strip": 1, "bin": "bin"}}},
			}},
			"kotlin": map[string]any{"requires": []string{"java25"}, "versions": map[string]any{
				"2.4.20": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/kotlin.zip", "sha256": sha(kotlin), "format": "zip", "strip": 1, "bin": "bin"}}},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	bins, err := in.Ensure(context.Background(), []Spec{{Name: "kotlin", Version: "2.4.20"}})
	if err != nil {
		t.Fatal(err)
	}
	// The requires (java25) must be pulled in automatically.
	if _, err := os.Stat(filepath.Join(root, "java25", "25.0.4", "bin", "java")); err != nil {
		t.Fatalf("required java25 not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "kotlin", "2.4.20", "bin", "kotlinc")); err != nil {
		t.Fatalf("kotlin not installed: %v", err)
	}
	if len(bins) != 2 {
		t.Fatalf("bins=%v want 2 (kotlin + java25)", bins)
	}
}

func TestEnsureInstallStep(t *testing.T) {
	blob := tarGz(t, "rust", map[string]string{"install.sh": "#!/bin/sh\ntouch \"$1/marker\"\n"})
	idxURL, _ := serveIndex(t, map[string][]byte{"rust.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"rust": map[string]any{"versions": map[string]any{
				"1.98.1": map[string]any{
					"artifacts": []any{map[string]any{
						"url": base + "/blobs/rust.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1}},
					"install": []string{"/bin/sh", "./install.sh", "{root}"},
				},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	if _, err := in.Ensure(context.Background(), []Spec{{Name: "rust", Version: "1.98.1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "rust", "1.98.1", "marker")); err != nil {
		t.Fatalf("install step did not run: %v", err)
	}
}

// TestEnsureRename: a top-level dir is renamed after unpack (dart's
// "dart-sdk->dart") so the declared `bin` resolves.
func TestEnsureRename(t *testing.T) {
	blob := zipSingle(t, "dart-sdk/bin/dart", "#!/bin/sh\n")
	idxURL, _ := serveIndex(t, map[string][]byte{"dart.zip": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"dart": map[string]any{"versions": map[string]any{
				"3.13.4": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/dart.zip", "sha256": sha(blob), "format": "zip", "strip": 0,
					"bin": "dart/bin", "rename": "dart-sdk->dart",
				}}},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	bins, err := in.Ensure(context.Background(), []Spec{{Name: "dart", Version: "3.13.4"}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "dart", "3.13.4", "dart", "bin")
	if len(bins) != 1 || bins[0] != want {
		t.Fatalf("bins=%v want [%s]", bins, want)
	}
	if _, err := os.Stat(filepath.Join(want, "dart")); err != nil {
		t.Fatalf("renamed bin not found: %v", err)
	}
}

func TestParseSpecs(t *testing.T) {
	specs, err := ParseSpecs("go=1.27.1, node=26.9.0\n# comment\n\npython=3.14.7")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 || specs[0] != (Spec{"go", "1.27.1"}) || specs[2] != (Spec{"python", "3.14.7"}) {
		t.Fatalf("specs=%v", specs)
	}
	if _, err := ParseSpecs("garbage"); err == nil {
		t.Fatal("expected error for spec without version")
	}
}

func TestGreatestVersion(t *testing.T) {
	v := map[string]Version{"1.9.0": {}, "1.10.0": {}, "1.27.1": {}}
	got, err := greatestVersion(v)
	if err != nil || got != "1.27.1" {
		t.Fatalf("got %q err %v", got, err)
	}
}

// TestEnsureEnv: a version's `env` is exported (with {root} expanded) after a
// successful install — ruby's LD_LIBRARY_PATH.
func TestEnsureEnv(t *testing.T) {
	blob := tarGz(t, "ruby", map[string]string{"bin/ruby": "#!/bin/sh\n"})
	idxURL, _ := serveIndex(t, map[string][]byte{"ruby.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"ruby": map[string]any{"versions": map[string]any{
				"4.0.7": map[string]any{
					"artifacts": []any{map[string]any{
						"url": base + "/blobs/ruby.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1, "bin": "bin"}},
					"env": map[string]string{"LD_LIBRARY_PATH": "{root}/lib"},
				},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	t.Setenv("LD_LIBRARY_PATH", "")
	if _, err := in.Ensure(context.Background(), []Spec{{Name: "ruby", Version: "4.0.7"}}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "ruby", "4.0.7", "lib")
	if got := os.Getenv("LD_LIBRARY_PATH"); got != want {
		t.Fatalf("env = %q, want %q", got, want)
	}
	// Merged, not replaced: a second contributor is prepended.
	other := filepath.Join(root, "clang", "lib")
	applyEnv(root, map[string]string{"LD_LIBRARY_PATH": other})
	if got := os.Getenv("LD_LIBRARY_PATH"); got != other+string(os.PathListSeparator)+want {
		t.Fatalf("merged LD_LIBRARY_PATH = %q", got)
	}
}

// TestEnsureInstallStepArgv: `install` argv runs with {root} expanded and cwd
// at the version dir.
func TestEnsureInstallStepArgv(t *testing.T) {
	blob := tarGz(t, "clj", map[string]string{"install.sh": "#!/bin/sh\nmkdir -p \"$1/bin\"; touch \"$1/bin/clojure\"\n"})
	idxURL, _ := serveIndex(t, map[string][]byte{"clj.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"clojure": map[string]any{"versions": map[string]any{
				"1.12.6": map[string]any{
					"artifacts": []any{map[string]any{
						"url": base + "/blobs/clj.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1, "bin": "bin"}},
					"install": []string{"/bin/sh", "./install.sh", "{root}"},
				},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	if _, err := in.Ensure(context.Background(), []Spec{{Name: "clojure", Version: "1.12.6"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "clojure", "1.12.6", "bin", "clojure")); err != nil {
		t.Fatalf("install step did not populate bin: %v", err)
	}
}

// TestEnsureUnpackDir: `unpack_dir` unpacks the archive into a subdir, runs
// install[] there with cwd=subdir, expands {root} to the VERSION ROOT, and
// resolves `bin` against the version root (rust's install.sh shape).
func TestEnsureUnpackDir(t *testing.T) {
	// install.sh asserts it is NOT run from its own directory, then writes to
	// the prefix it is given.
	blob := tarGz(t, "rust", map[string]string{
		"install.sh": "#!/bin/sh\np=\"${1#--prefix=}\"\n[ \"$PWD\" != \"$p\" ] || { echo same-dir >&2; exit 1; }\nmkdir -p \"$p/bin\"; touch \"$p/bin/rustc\"\n",
	})
	idxURL, _ := serveIndex(t, map[string][]byte{"rust.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"rust": map[string]any{"versions": map[string]any{
				"1.98.1": map[string]any{
					"artifacts": []any{map[string]any{
						"url": base + "/blobs/rust.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1, "bin": "bin"}},
					"install":    []string{"/bin/sh", "./install.sh", "--prefix={root}"},
					"unpack_dir": "dist",
				},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	bins, err := in.Ensure(context.Background(), []Spec{{Name: "rust", Version: "1.98.1"}})
	if err != nil {
		t.Fatal(err)
	}
	// bin resolves against the VERSION ROOT (install[] wrote there), not dist/.
	want := filepath.Join(root, "rust", "1.98.1", "bin")
	if len(bins) != 1 || bins[0] != want {
		t.Fatalf("bins=%v want [%s]", bins, want)
	}
	if _, err := os.Stat(filepath.Join(want, "rustc")); err != nil {
		t.Fatalf("install did not populate bin at version root: %v", err)
	}
}

// TestEnsureExecutableBit: a bin file shipped non-executable is made runnable.
func TestEnsureExecutableBit(t *testing.T) {
	blob := tarGz(t, "perl", map[string]string{"bin/cpanm": "#!/bin/sh\necho hi\n"})
	idxURL, _ := serveIndex(t, map[string][]byte{"perl.tgz": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"perl": map[string]any{"versions": map[string]any{
				"1.7049": map[string]any{"artifacts": []any{map[string]any{
					"url": base + "/blobs/perl.tgz", "sha256": sha(blob), "format": "tar.gz", "strip": 1, "bin": "bin"}}},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	if _, err := in.Ensure(context.Background(), []Spec{{Name: "perl", Version: "1.7049"}}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "perl", "1.7049", "bin", "cpanm"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("cpanm not executable: %v", fi.Mode())
	}
}

// TestEnsureInstallPrefix: install[]'s {root} and `bin` resolve against
// <version-root>/<install_prefix> (the installer's OUTPUT dir) — conda's shape.
func TestEnsureInstallPrefix(t *testing.T) {
	// raw installer script that writes <prefix>/bin/tool, refusing an existing prefix.
	blob := []byte("#!/bin/sh\np=\"$3\"\n[ ! -e \"$p\" ] || { echo exists >&2; exit 1; }\nmkdir -p \"$p/bin\"; touch \"$p/bin/tool\"\n")
	idxURL, _ := serveIndex(t, map[string][]byte{"inst.sh": blob}, func(base string) string {
		doc := map[string]any{"schema": 1, "toolchains": map[string]any{
			"conda": map[string]any{"versions": map[string]any{
				"latest": map[string]any{
					"artifacts": []any{map[string]any{
						"url": base + "/blobs/inst.sh", "sha256": sha(blob), "format": "raw", "strip": 0, "bin": "bin/inst.sh"}},
					"install":        []string{"sh", "-c", "bash bin/inst.sh -b -p miniconda"},
					"install_prefix": "miniconda",
				},
			}},
		}}
		b, _ := json.Marshal(doc)
		return string(b)
	})
	root := t.TempDir()
	in := New(root, idxURL, http.DefaultClient, nil)
	bins, err := in.Ensure(context.Background(), []Spec{{Name: "conda", Version: "latest"}})
	if err != nil {
		t.Fatal(err)
	}
	// bin is relative to <root>/miniconda -> <root>/miniconda/bin.
	want := filepath.Join(root, "conda", "latest", "miniconda", "bin")
	if len(bins) != 1 || bins[0] != want {
		t.Fatalf("bins=%v want [%s]", bins, want)
	}
	if _, err := os.Stat(filepath.Join(want, "tool")); err != nil {
		t.Fatalf("install did not populate the prefix bin: %v", err)
	}
}
