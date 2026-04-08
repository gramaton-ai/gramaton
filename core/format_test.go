package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brandonlattin/gramaton/internal/version"
)

func TestReadFormatVersionMissing(t *testing.T) {
	dir := t.TempDir()
	v, err := ReadFormatVersion(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 0 {
		t.Fatalf("expected 0 for missing FORMAT, got %d", v)
	}
}

func TestWriteAndReadFormatVersion(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFormatVersion(dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	v, err := ReadFormatVersion(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if v != version.StoreFormatVersion {
		t.Fatalf("expected %d, got %d", version.StoreFormatVersion, v)
	}
}

func TestCheckFormatVersionNewStore(t *testing.T) {
	dir := t.TempDir()
	if err := CheckFormatVersion(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have written FORMAT file.
	v, err := ReadFormatVersion(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if v != version.StoreFormatVersion {
		t.Fatalf("expected %d, got %d", version.StoreFormatVersion, v)
	}
}

func TestCheckFormatVersionCompatible(t *testing.T) {
	dir := t.TempDir()
	// Write current version.
	os.WriteFile(filepath.Join(dir, "FORMAT"), []byte("1"), 0o600)

	if err := CheckFormatVersion(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckFormatVersionTooNew(t *testing.T) {
	dir := t.TempDir()
	// Write a future version.
	os.WriteFile(filepath.Join(dir, "FORMAT"), []byte("999"), 0o600)

	err := CheckFormatVersion(dir)
	if err == nil {
		t.Fatal("expected error for newer format version")
	}
	if !contains(err.Error(), "newer than this binary") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCheckFormatVersionInvalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "FORMAT"), []byte("not-a-number"), 0o600)

	_, err := ReadFormatVersion(dir)
	if err == nil {
		t.Fatal("expected error for invalid FORMAT")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
