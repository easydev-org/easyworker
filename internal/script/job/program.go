package job

import (
	"os"
	"path/filepath"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// materializeProgram decides how the program reaches the interpreter.
//
// The API only ever carries program TEXT — a caller can never name a file on
// the daemon's disk. So:
//
//   - when the interpreter accepts source on the command line (ProgFlag != ""),
//     the text is passed as-is and there is nothing to clean up;
//   - when it can only read a FILE, the text is written to a temporary file in
//     the job's working directory, and the returned cleanup removes it once the
//     process has exited.
//
// It returns (source, filePath, cleanup). cleanup is never nil.
func materializeProgram(l lang.Lang, spec Spec) (string, string, func(), error) {
	noop := func() {}
	if l.ProgFlag != "" {
		return spec.Code, "", noop, nil
	}
	dir := spec.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "easyscript-*"+scriptSuffix(l))
	if err != nil {
		return "", "", noop, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.WriteString(spec.Code); err != nil {
		f.Close()
		cleanup()
		return "", "", noop, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", "", noop, err
	}
	return spec.Code, path, cleanup, nil
}

// scriptSuffix gives the temp file an extension the interpreter recognises
// (e.g. ".py"), so it does not guess the language from a suffix-less name.
func scriptSuffix(l lang.Lang) string {
	// Delegate to the registry: a language can need a specific temp-file suffix
	// without claiming that extension for path-based detection (Windows
	// PowerShell needs *.ps1, but .ps1 belongs to PowerShell 7).
	return l.TempSuffix()
}

// scriptName is the file name of a materialized program ("" when inline).
func scriptName(filePath string) string {
	if filePath == "" {
		return ""
	}
	return filepath.Base(filePath)
}
