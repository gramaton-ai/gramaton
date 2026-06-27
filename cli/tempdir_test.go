package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// Ensure temp dir exists so EvalSymlinks can resolve it.
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	// Create a subdirectory so the nested path test resolves.
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	defer os.Remove(subDir)

	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "test.json"), true},
		{filepath.Join(dir, "sub", "test.json"), true},
		{dir, false}, // the dir itself
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

func TestIsInTempDir_TraversalVariants(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"double dot mid-path", filepath.Join(dir, "a", "..", "b.json"), true},
		{"double dot escape", filepath.Join(dir, "..", "escape.json"), false},
		{"dot only", filepath.Join(dir, "."), false},
		{"absolute unrelated", filepath.Join(string(filepath.Separator), "tmp", "other"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInTempDir(tt.path)
			if got != tt.want {
				t.Errorf("isInTempDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSweepRemovesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		// os.Symlink on Windows requires the SeCreateSymbolicLink
		// privilege (admin or Developer Mode), which CI runners
		// don't provide. The sweep behavior is exercised on
		// Linux/macOS CI; the production path is OS-agnostic.
		t.Skip("os.Symlink on Windows requires admin or Developer Mode")
	}
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	// Create a symlink inside the temp dir.
	linkName := fmt.Sprintf("symlink-test-%d", time.Now().UnixNano())
	linkPath := filepath.Join(dir, linkName)
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "target.json")
	if err := os.WriteFile(targetPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	sweepStaleTempFiles()

	// Symlink should be removed regardless of age.
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Error("symlink should have been removed by sweep")
		_ = os.Remove(linkPath) // clean up on failure
	}

	// Target should be untouched.
	if _, err := os.Stat(targetPath); err != nil {
		t.Error("symlink target should not be deleted")
	}
}
