package mmap_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/mmap"
)

func writeTempFile(t *testing.T, content []byte) *os.File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestOpenRoundTrip(t *testing.T) {
	want := []byte("hello from mmap")
	f := writeTempFile(t, want)

	r, err := mmap.Open(f, len(want))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got := r.Bytes()
	if !bytes.Equal(got, want) {
		t.Errorf("Bytes() = %q, want %q", got, want)
	}
}

func TestOpenEmptyFile(t *testing.T) {
	f := writeTempFile(t, nil)

	_, err := mmap.Open(f, 0)
	if err == nil {
		t.Fatal("Open on empty file should error")
	}
}

func TestOpenNegativeSize(t *testing.T) {
	f := writeTempFile(t, []byte("x"))

	_, err := mmap.Open(f, -1)
	if err == nil {
		t.Fatal("Open with negative size should error")
	}
}

func TestOpenMultiPage(t *testing.T) {
	// 256 KiB — safely larger than any OS page size (4K on x86,
	// 16K on Apple Silicon). Verifies multi-page views work.
	size := 256 * 1024
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 256)
	}
	f := writeTempFile(t, content)

	r, err := mmap.Open(f, size)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got := r.Bytes()
	if len(got) != size {
		t.Fatalf("len(Bytes()) = %d, want %d", len(got), size)
	}
	// Spot-check first/middle/last bytes to confirm the full
	// range is mapped, not just the first page.
	if got[0] != 0 || got[size/2] != byte((size/2)%256) || got[size-1] != byte((size-1)%256) {
		t.Error("multi-page content mismatch")
	}
}

func TestCloseIdempotent(t *testing.T) {
	f := writeTempFile(t, []byte("abc"))
	r, err := mmap.Open(f, 3)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close should be no-op, got %v", err)
	}
}

func TestCloseNilRegion(t *testing.T) {
	// Close on a nil *Region must not panic — mirrors the
	// idiom of `defer r.Close()` after an `Open` that might
	// have failed and returned nil.
	var r *mmap.Region
	if err := r.Close(); err != nil {
		t.Errorf("Close on nil Region = %v, want nil", err)
	}
}

func TestBytesNilRegion(t *testing.T) {
	var r *mmap.Region
	if got := r.Bytes(); got != nil {
		t.Errorf("Bytes on nil Region = %v, want nil", got)
	}
}
