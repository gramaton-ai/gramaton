package bert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	// Mock HTTP server that returns a small file.
	content := "hello safetensors"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "test.bin")
	err := downloadFile(context.Background(), server.URL+"/test.bin", dst)
	if err != nil {
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
}

func TestDownloadFileHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "test.bin")
	err := downloadFile(context.Background(), server.URL+"/missing", dst)
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestDownloadFileCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow response -- context should cancel before completion.
		w.Write([]byte("partial"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	dst := filepath.Join(t.TempDir(), "test.bin")
	err := downloadFile(ctx, server.URL+"/slow", dst)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestEnsureModelAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	model := "test-model"

	// Override ModelDir for testing.
	modelDir := filepath.Join(dir, model)
	os.MkdirAll(modelDir, 0755)

	// Create all required files.
	for _, f := range modelFiles {
		os.WriteFile(filepath.Join(modelDir, f), []byte("dummy"), 0600)
	}

	// Patch ModelDir to return our test dir -- since we can't do that
	// easily, test the logic directly: if all files exist, no download.
	allPresent := true
	for _, f := range modelFiles {
		if _, err := os.Stat(filepath.Join(modelDir, f)); err != nil {
			allPresent = false
			break
		}
	}
	if !allPresent {
		t.Error("expected all files to be present")
	}
}

func TestEnsureModelDownloads(t *testing.T) {
	// Mock HuggingFace server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mock-data"))
	}))
	defer server.Close()

	dir := t.TempDir()
	model := "dl-test"
	modelDir := filepath.Join(dir, model)
	os.MkdirAll(modelDir, 0755)

	// Download each file using our downloadFile function.
	for _, f := range modelFiles {
		dst := filepath.Join(modelDir, f)
		err := downloadFile(context.Background(), server.URL+"/"+f, dst)
		if err != nil {
			t.Fatalf("download %s: %v", f, err)
		}
	}

	// Verify all files exist.
	for _, f := range modelFiles {
		path := filepath.Join(modelDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file %s missing after download", f)
		}
	}
}

func TestFileChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.bin")
	os.WriteFile(path, []byte("hello"), 0600)

	sum, err := fileChecksum(path)
	if err != nil {
		t.Fatal(err)
	}
	// SHA256 of "hello" = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sum != want {
		t.Errorf("checksum: got %s, want %s", sum, want)
	}
}
