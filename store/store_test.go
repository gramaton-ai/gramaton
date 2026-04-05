package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"work", "personal", "my-store", "store_1", "A", "a1b2c3"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"default",   // reserved
		"",          // empty
		"-leading",  // starts with hyphen
		"_leading",  // starts with underscore
		"has space",
		"has/slash",
		"has.dot",
		"has@sign",
		string(make([]byte, 65)), // too long (65 chars of nulls won't match)
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
}

func TestValidateNameMaxLength(t *testing.T) {
	// Exactly 64 chars should be valid.
	name64 := "a" + string(make([]byte, 63))
	// Fill with valid chars.
	valid64 := make([]byte, 64)
	for i := range valid64 {
		valid64[i] = 'a'
	}
	if err := ValidateName(string(valid64)); err != nil {
		t.Errorf("64-char name should be valid: %v", err)
	}

	// 65 chars should be invalid.
	valid65 := make([]byte, 65)
	for i := range valid65 {
		valid65[i] = 'a'
	}
	if err := ValidateName(string(valid65)); err == nil {
		t.Error("65-char name should be invalid")
	}
	_ = name64
}

func TestResolve(t *testing.T) {
	base := "/home/user/.gramaton"

	if got := Resolve(base, ""); got != base {
		t.Errorf("Resolve(base, '') = %q, want %q", got, base)
	}

	want := filepath.Join(base, "stores", "work")
	if got := Resolve(base, "work"); got != want {
		t.Errorf("Resolve(base, 'work') = %q, want %q", got, want)
	}
}

func TestCreateAndExists(t *testing.T) {
	base := t.TempDir()

	if Exists(base, "work") {
		t.Fatal("store should not exist before creation")
	}

	if err := Create(base, "work"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !Exists(base, "work") {
		t.Fatal("store should exist after creation")
	}

	// Data directory should exist.
	dataDir := filepath.Join(base, "stores", "work", "data")
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data dir should exist: %v", err)
	}
}

func TestCreateDuplicate(t *testing.T) {
	base := t.TempDir()

	if err := Create(base, "work"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := Create(base, "work"); err == nil {
		t.Fatal("duplicate create should fail")
	}
}

func TestCreateInvalidName(t *testing.T) {
	base := t.TempDir()

	if err := Create(base, "default"); err == nil {
		t.Fatal("should reject 'default'")
	}

	if err := Create(base, "has/slash"); err == nil {
		t.Fatal("should reject invalid name")
	}
}

func TestDelete(t *testing.T) {
	base := t.TempDir()

	Create(base, "work")

	if err := Delete(base, "work"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if Exists(base, "work") {
		t.Fatal("store should not exist after deletion")
	}
}

func TestDeleteNonexistent(t *testing.T) {
	base := t.TempDir()

	if err := Delete(base, "nonexistent"); err == nil {
		t.Fatal("delete nonexistent should fail")
	}
}

func TestDefaultExists(t *testing.T) {
	base := t.TempDir()

	if DefaultExists(base) {
		t.Fatal("default should not exist initially")
	}

	os.MkdirAll(filepath.Join(base, "data"), 0o700)

	if !DefaultExists(base) {
		t.Fatal("default should exist after creating data/")
	}
}

func TestList(t *testing.T) {
	base := t.TempDir()

	// Empty list.
	stores := List(base)
	if len(stores) != 0 {
		t.Fatalf("expected 0 stores, got %d", len(stores))
	}

	// Add default.
	os.MkdirAll(filepath.Join(base, "data"), 0o700)
	stores = List(base)
	if len(stores) != 1 || !stores[0].Default {
		t.Fatalf("expected 1 default store, got %v", stores)
	}

	// Add named stores.
	Create(base, "beta")
	Create(base, "alpha")
	stores = List(base)
	if len(stores) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(stores))
	}
	// Default first, then alphabetical.
	if !stores[0].Default {
		t.Fatal("first should be default")
	}
	if stores[1].Name != "alpha" {
		t.Fatalf("second should be alpha, got %q", stores[1].Name)
	}
	if stores[2].Name != "beta" {
		t.Fatalf("third should be beta, got %q", stores[2].Name)
	}
}

func TestRenameNamedToNamed(t *testing.T) {
	base := t.TempDir()
	Create(base, "old")

	// Write a file so we can verify it moved.
	writeFile(t, filepath.Join(base, "stores", "old", "data", "test.txt"), "hello")

	if err := Rename(base, "old", "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if Exists(base, "old") {
		t.Fatal("old should not exist")
	}
	if !Exists(base, "new") {
		t.Fatal("new should exist")
	}

	// Verify data moved.
	data, err := os.ReadFile(filepath.Join(base, "stores", "new", "data", "test.txt"))
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestRenameDefaultToNamed(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "data"), 0o700)
	writeFile(t, filepath.Join(base, "data", "HEAD"), "abc123")

	if err := Rename(base, "default", "work"); err != nil {
		t.Fatalf("Rename default->work: %v", err)
	}

	if DefaultExists(base) {
		t.Fatal("default should not exist after rename")
	}
	if !Exists(base, "work") {
		t.Fatal("work should exist after rename")
	}

	// Verify data moved.
	data, err := os.ReadFile(filepath.Join(base, "stores", "work", "data", "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if string(data) != "abc123" {
		t.Fatalf("HEAD content mismatch")
	}
}

func TestRenameNamedToDefault(t *testing.T) {
	base := t.TempDir()
	Create(base, "work")
	writeFile(t, filepath.Join(base, "stores", "work", "data", "HEAD"), "def456")

	if err := Rename(base, "work", "default"); err != nil {
		t.Fatalf("Rename work->default: %v", err)
	}

	if Exists(base, "work") {
		t.Fatal("work should not exist after rename")
	}
	if !DefaultExists(base) {
		t.Fatal("default should exist after rename")
	}

	data, err := os.ReadFile(filepath.Join(base, "data", "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if string(data) != "def456" {
		t.Fatalf("HEAD content mismatch")
	}
}

func TestRenameToExisting(t *testing.T) {
	base := t.TempDir()
	Create(base, "a")
	Create(base, "b")

	if err := Rename(base, "a", "b"); err == nil {
		t.Fatal("rename to existing should fail")
	}
}

func TestRenameNonexistent(t *testing.T) {
	base := t.TempDir()

	if err := Rename(base, "nonexistent", "new"); err == nil {
		t.Fatal("rename nonexistent should fail")
	}
}

func TestRenameSameName(t *testing.T) {
	base := t.TempDir()
	Create(base, "same")

	if err := Rename(base, "same", "same"); err == nil {
		t.Fatal("rename to same name should fail")
	}
}

func TestRenameDefaultToDefaultBlocked(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "data"), 0o700)

	// Can't rename default to default.
	if err := Rename(base, "default", "default"); err == nil {
		t.Fatal("rename default to default should fail")
	}
}

func TestRenameNamedToDefaultBlockedWhenDefaultExists(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "data"), 0o700)
	Create(base, "work")

	if err := Rename(base, "work", "default"); err == nil {
		t.Fatal("should fail when default already has data")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
