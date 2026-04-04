package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTempDir(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	expected := filepath.Join(os.TempDir(), tempSubdir)
	if dir != expected {
		t.Errorf("TempDir = %q, want %q", dir, expected)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("TempDir result is not a directory")
	}
}

func TestIsInTempDir(t *testing.T) {
	dir := filepath.Join(os.TempDir(), tempSubdir)

	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "test.json"), true},
		{filepath.Join(dir, "sub", "test.json"), true},
		{dir, false},                                      // the dir itself
		{filepath.Join(os.TempDir(), "other.json"), false}, // sibling
		{"/etc/passwd", false},                             // unrelated
		{filepath.Join(dir, "..", "escape.json"), false},   // traversal
	}

	for _, tt := range tests {
		got := isInTempDir(tt.path)
		if got != tt.want {
			t.Errorf("isInTempDir(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestSweepStaleTempFiles(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	staleName := fmt.Sprintf("stale-test-%d.json", time.Now().UnixNano())
	freshName := fmt.Sprintf("fresh-test-%d.json", time.Now().UnixNano())

	stalePath := filepath.Join(dir, staleName)
	if err := os.WriteFile(stalePath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	freshPath := filepath.Join(dir, freshName)
	if err := os.WriteFile(freshPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write fresh file: %v", err)
	}

	sweepStaleTempFiles()

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale file should have been removed")
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Error("fresh file should still exist")
	}

	_ = os.Remove(freshPath)
}
