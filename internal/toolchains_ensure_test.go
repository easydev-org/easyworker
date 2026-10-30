package internal

import (
	"archive/tar"
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
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	workerv1connect "github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyworker/internal/filesvc"
	"github.com/easydev-org/easyworker/internal/jobsvc"
	"github.com/easydev-org/easyworker/internal/script"
	"github.com/easydev-org/easyworker/internal/toolchains"
)

// TestExecuteEnsuresDeclaredToolchain is the end-to-end contract: a toolchain
// declared via WORKSPACE_TOOLCHAINS is installed before the job runs, and the
// job resolves the newly installed binary.
func TestExecuteEnsuresDeclaredToolchain(t *testing.T) {
	ws := t.TempDir()
	blob := makeToolTarGz(t, "go", map[string]string{"bin/mytool": "#!/bin/sh\necho from-toolchain\n"})
	idxURL := writeIndex(t, ws, blob, sha256Hex(blob))

	runner := script.New(ws)
	store, err := jobsvc.OpenStore(filepath.Join(ws, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installer := toolchains.New(filepath.Join(ws, "opt"), idxURL, http.DefaultClient, nil)
	svc := NewService(jobsvc.NewManager(runner, store, 1000, 200), filesvc.New(ws), runner, installer)

	mux := http.NewServeMux()
	server := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(server, svc)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := workerv1connect.NewWorkerServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, srv.URL, connecthttp.WithReadMaxBytes(0))))

	t.Setenv("WORKSPACE_TOOLCHAINS", "go=1.27.1")
	exec, err := c.Execute(context.Background(), &workerv1.ExecuteRequest{Lang: shellLang(t, c), Command: "mytool"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	waitJob(t, c, exec.JobId)
	out, err := c.JobOutput(context.Background(), &workerv1.JobOutputRequest{JobId: exec.JobId})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(out.Lines, "\n"), "from-toolchain") {
		t.Fatalf("job output = %v", out.Lines)
	}
}

// TestExecuteToolchainFailureFailsJob: a declared-but-uninstallable toolchain
// must fail the Execute loudly, never run the job against the bare base.
func TestExecuteToolchainFailureFailsJob(t *testing.T) {
	ws := t.TempDir()
	// Index with a sha that cannot match the served blob.
	blob := makeToolTarGz(t, "go", map[string]string{"bin/mytool": "x"})
	idxURL := writeIndex(t, ws, blob, strings.Repeat("0", 64))

	runner := script.New(ws)
	store, err := jobsvc.OpenStore(filepath.Join(ws, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installer := toolchains.New(filepath.Join(ws, "opt"), idxURL, http.DefaultClient, nil)
	svc := NewService(jobsvc.NewManager(runner, store, 1000, 200), filesvc.New(ws), runner, installer)
	mux := http.NewServeMux()
	server := connect.NewServer()
	workerv1connect.RegisterWorkerServiceHandler(server, svc)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := workerv1connect.NewWorkerServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, srv.URL, connecthttp.WithReadMaxBytes(0))))

	t.Setenv("WORKSPACE_TOOLCHAINS", "go=1.27.1")
	if _, err := c.Execute(context.Background(), &workerv1.ExecuteRequest{Lang: shellLang(t, c), Command: "true"}); err == nil {
		t.Fatal("expected Execute to fail when the toolchain cannot be installed")
	}
}

func waitJob(t *testing.T, c workerv1connect.WorkerServiceClient, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.JobOutput(context.Background(), &workerv1.JobOutputRequest{JobId: id})
		if err == nil && out.Done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
}

func makeToolTarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		b := []byte(body)
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))}); err != nil {
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

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// writeIndex writes the blob next to a local index.json and returns the index
// path (the installer accepts a scheme-less path).
func writeIndex(t *testing.T, dir string, blob []byte, sha string) string {
	t.Helper()
	blobPath := filepath.Join(dir, "go.tgz")
	if err := os.WriteFile(blobPath, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"schema": 1, "toolchains": map[string]any{
		"go": map[string]any{"versions": map[string]any{
			"1.27.1": map[string]any{"artifacts": []any{map[string]any{
				"url": blobPath, "sha256": sha, "format": "tar.gz", "strip": 1, "bin": "bin",
			}}},
		}},
	}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, "index.json")
	if err := os.WriteFile(idxPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return idxPath
}
