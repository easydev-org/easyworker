package toolchains

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// stripPath drops `strip` leading path components. ok=false when the entry is a
// pure parent dir (too shallow) and must be skipped.
func stripPath(name string, strip int) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	parts := strings.Split(name, "/")
	if strip > 0 {
		if len(parts) <= strip {
			return "", false
		}
		parts = parts[strip:]
	}
	joined := strings.Join(parts, "/")
	if joined == "" || joined == "." {
		return "", false
	}
	return joined, true
}

// safeJoin joins dest with an archive-relative path, refusing escapes.
func safeJoin(dest, rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	if rel == "" {
		return "", fmt.Errorf("empty archive path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path in archive: %s", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes archive root: %s", rel)
	}
	return filepath.Join(dest, clean), nil
}

// extractTar unpacks a tar stream into dest, dropping `strip` top components.
// Symlinks and hardlinks are preserved (toolchain tarballs use them heavily).
func extractTar(r io.Reader, dest string, strip int) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok := stripPath(hdr.Name, strip)
		if !ok {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFileFrom(target, tr, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			lrel, ok := stripPath(hdr.Linkname, strip)
			if !ok {
				continue
			}
			src, err := safeJoin(dest, lrel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(src, target); err != nil {
				return err
			}
		}
	}
}

// extractZip unpacks a zip archive into dest, dropping `strip` top components.
func extractZip(path, dest string, strip int) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		rel, ok := stripPath(f.Name, strip)
		if !ok {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		err = writeFileFrom(target, rc, mode&0o777)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFileFrom(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// fileStem is the URL basename with one known compression suffix removed.
func fileStem(rawurl string) string {
	u := rawurl
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := u[strings.LastIndex(u, "/")+1:]
	for _, ext := range []string{".tar.gz", ".tar.xz", ".tgz", ".zip", ".gz", ".xz", ".phar"} {
		if strings.HasSuffix(base, ext) {
			return strings.TrimSuffix(base, ext)
		}
	}
	return base
}

func expandRoot(s, root string) string { return strings.ReplaceAll(s, "{root}", root) }

// applyRename renames one unpacked entry under the version root. spec is
// "src->dst" (both relative to root, after strip). Both ends must stay inside
// root; the source must exist.
func applyRename(root, spec string) error {
	src, dst, ok := strings.Cut(spec, "->")
	if !ok {
		return fmt.Errorf("rename %q must be src->dst", spec)
	}
	src, dst = strings.TrimSpace(src), strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return fmt.Errorf("rename %q has an empty side", spec)
	}
	from, err := safeJoin(root, src)
	if err != nil {
		return fmt.Errorf("rename %q: %w", spec, err)
	}
	to, err := safeJoin(root, dst)
	if err != nil {
		return fmt.Errorf("rename %q: %w", spec, err)
	}
	if _, err := os.Lstat(from); err != nil {
		return fmt.Errorf("rename %q: source missing: %w", spec, err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(to)
	return os.Rename(from, to)
}
