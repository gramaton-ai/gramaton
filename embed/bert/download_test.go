package bert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelDir(t *testing.T) {
	dir := ModelDir("test-model")
	if dir == "" {
		t.Error("ModelDir returned empty string")
	}
	if filepath.Base(dir) != "test-model" {
		t.Errorf("ModelDir base: got %q, want %q", filepath.Base(dir), "test-model")
	}
}

func TestDownloadFile(t *testing.T) {
	content := "hello safetensors"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "test.bin")
	if err := downloadFile(context.Background(), server.URL+"/test.bin", dst); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content: got %q, want %q", data, content)
	}

	// Verify no .tmp file left behind.
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not exist after successful download")
	}

	// Verify sidecar was written with the correct hash.
	sidecar, err := os.ReadFile(dst + sidecarSuffix)
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	if want, _ := fileChecksum(dst); string(sidecar) != want {
		t.Errorf("sidecar mismatch: got %s, want %s", sidecar, want)
	}
}

func TestDownloadFileHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "test.bin")
	if err := downloadFile(context.Background(), server.URL+"/missing", dst); err == nil {
		t.Error("expected error for 404")
	}
}

func TestDownloadFileCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("partial"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dst := filepath.Join(t.TempDir(), "test.bin")
	if err := downloadFile(ctx, server.URL+"/slow", dst); err == nil {
		t.Error("expected error for cancelled context")
	}
}

// TestDownloadFileTruncatedContentLength covers the truncation case
// (server declares one length, sends fewer bytes). Net/http's chunked
// fallback can mask this; the explicit Content-Length check is the
// guard.
func TestDownloadFileTruncatedContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lie about the size so the client sees a truncated body.
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("only-13-bytes"))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "test.bin")
	err := downloadFile(context.Background(), server.URL+"/x", dst)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	// Either the size mismatch fires, OR io.Copy fails because the
	// connection closed before Content-Length bytes arrived. Both are
	// acceptable -- the file must NOT exist either way.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("truncated download should not leave file in place; err=%v", err)
	}
}

func TestFileChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.bin")
	os.WriteFile(path, []byte("hello"), 0600)

	sum, err := fileChecksum(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sum != want {
		t.Errorf("checksum: got %s, want %s", sum, want)
	}
}

// TestVerifyOrBootstrapSidecarBootstrap covers the TOFU path: file
// exists without a sidecar (legacy or hand-placed). We compute the
// hash, write the sidecar, log a warning.
func TestVerifyOrBootstrapSidecarBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	os.WriteFile(path, []byte("hello"), 0600)

	if err := verifyOrBootstrapSidecar(path); err != nil {
		t.Fatalf("bootstrap should succeed: %v", err)
	}

	sidecar, err := os.ReadFile(path + sidecarSuffix)
	if err != nil {
		t.Fatalf("sidecar not bootstrapped: %v", err)
	}
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if string(sidecar) != want {
		t.Errorf("bootstrapped sidecar: got %s, want %s", sidecar, want)
	}
}

// TestVerifyOrBootstrapSidecarMatch covers the happy path: sidecar
// exists and matches the file.
func TestVerifyOrBootstrapSidecarMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	os.WriteFile(path, []byte("hello"), 0600)
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	os.WriteFile(path+sidecarSuffix, []byte(want), 0600)

	if err := verifyOrBootstrapSidecar(path); err != nil {
		t.Errorf("match should succeed: %v", err)
	}
}

// TestVerifyOrBootstrapSidecarMismatch covers on-disk corruption:
// file bytes differ from the recorded sidecar. Both file and sidecar
// must be quarantined and a clear error returned.
func TestVerifyOrBootstrapSidecarMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	// File contains "world" but sidecar says it should be "hello".
	os.WriteFile(path, []byte("world"), 0600)
	helloHash := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	os.WriteFile(path+sidecarSuffix, []byte(helloHash), 0600)

	err := verifyOrBootstrapSidecar(path)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Errorf("error should mention integrity: %v", err)
	}

	// Original file should be quarantined.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("original file should be quarantined (renamed away)")
	}
	// Sidecar should also be quarantined.
	if _, statErr := os.Stat(path + sidecarSuffix); !os.IsNotExist(statErr) {
		t.Error("original sidecar should be quarantined")
	}
	// A .suspect.* file should exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "model.bin.suspect.*"))
	if len(matches) == 0 {
		t.Error("expected quarantined .suspect.* file to be preserved")
	}
}

// TestVerifyOrBootstrapSidecarTrailingNewline tolerates a sidecar
// with a trailing newline (some editors add one).
func TestVerifyOrBootstrapSidecarTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.bin")
	os.WriteFile(path, []byte("hello"), 0600)
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n"
	os.WriteFile(path+sidecarSuffix, []byte(want), 0600)

	if err := verifyOrBootstrapSidecar(path); err != nil {
		t.Errorf("trailing newline should be tolerated: %v", err)
	}
}
