package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readStoreDataDir parses a per-store config.yaml and returns its
// data_dir value. Parsing (rather than a raw substring match) is what
// makes the data_dir assertions cross-platform: on Windows the pinned
// path is written with escaped backslashes, so a filepath.Join'd
// expected path is not a literal substring of the file bytes.
func readStoreDataDir(t *testing.T, cfgPath string) (dataDir string, doc map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config %s: %v", cfgPath, err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse config %s: %v", cfgPath, err)
	}
	dataDir, _ = doc["data_dir"].(string)
	return dataDir, doc
}

func TestValidateName(t *testing.T) {
	valid := []string{"work", "personal", "my-store", "store_1", "A", "a1b2c3"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"default",  // reserved
		"",         // empty
		"-leading", // starts with hyphen
		"_leading", // starts with underscore
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

// TestRenameNamedToNamedRepinsDataDir pins the data_dir corruption bug:
// a named->named rename moves the store home (config.yaml included)
// verbatim, so the pinned ABSOLUTE data_dir would keep naming the OLD
// path unless Rename re-pins it. A user-added key must survive the
// rewrite. Fails against the pre-fix bare os.Rename.
func TestRenameNamedToNamedRepinsDataDir(t *testing.T) {
	base := t.TempDir()
	if err := Create(base, "old"); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(base, "stores", "old")
	if _, err := WriteDataDirConfig(oldDir, "old"); err != nil {
		t.Fatal(err)
	}
	// A hand-added override the repin must preserve.
	cfgPath := filepath.Join(oldDir, "config.yaml")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("log_level: debug\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := Rename(base, "old", "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	gotData, doc := readStoreDataDir(t, filepath.Join(base, "stores", "new", "config.yaml"))
	newData := filepath.Join(base, "stores", "new", "data")
	if gotData != newData {
		t.Errorf("data_dir = %q, want it re-pinned to the new path %q", gotData, newData)
	}
	oldData := filepath.Join(base, "stores", "old", "data")
	if gotData == oldData {
		t.Errorf("data_dir still names the OLD data path %q", oldData)
	}
	if _, ok := doc["log_level"]; !ok {
		t.Errorf("a user-added key was lost in the repin: %v", doc)
	}
}

// TestRenameDefaultToNamedWritesDataDirConfig pins the other half of
// the bug: renaming the default store to a named one moves only data/
// and previously wrote NO per-store config, so the named store had no
// data_dir pin and the global data_dir would bleed through. Fails
// against the pre-fix code (no config written).
func TestRenameDefaultToNamedWritesDataDirConfig(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "data", "HEAD"), "abc123")

	if err := Rename(base, "default", "work"); err != nil {
		t.Fatalf("Rename default->work: %v", err)
	}

	cfgPath := filepath.Join(base, "stores", "work", "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("default->named must write a per-store config pinning data_dir: %v", err)
	}
	gotData, _ := readStoreDataDir(t, cfgPath)
	wantData := filepath.Join(base, "stores", "work", "data")
	if gotData != wantData {
		t.Errorf("data_dir = %q, want %q", gotData, wantData)
	}
}

func TestListExistsRecognizeRemoteStore(t *testing.T) {
	base := t.TempDir()
	// A remote-client store: config.yaml with remote.url, no data/ dir.
	dir := filepath.Join(base, "stores", "team")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.yaml"), "remote:\n  url: https://lab.local:7420\n")

	if !Exists(base, "team") {
		t.Error("Exists should recognize a remote-only store (no data/ dir)")
	}
	var found *StoreInfo
	for i, s := range List(base) {
		if s.Name == "team" {
			found = &List(base)[i]
		}
	}
	if found == nil {
		t.Fatal("List should include the remote store")
	}
	if !found.Remote || found.RemoteURL != "https://lab.local:7420" {
		t.Errorf("remote store = %+v, want Remote=true with the URL", *found)
	}
	if found.Path != "" {
		t.Errorf("remote store Path = %q, want empty (data is on another machine)", found.Path)
	}
}

func TestListShowsRemoteDefaultStore(t *testing.T) {
	// A remote default store: base/config.yaml with remote.url, no
	// base/data. Previously invisible because the default branch only
	// checked base/data.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "config.yaml"), "remote:\n  url: https://lab.local:7420\n")

	stores := List(base)
	if len(stores) != 1 {
		t.Fatalf("want 1 store, got %d: %+v", len(stores), stores)
	}
	s := stores[0]
	if !s.Default || !s.Remote || s.RemoteURL != "https://lab.local:7420" {
		t.Errorf("default store = %+v, want Default+Remote with the URL", s)
	}
	if s.Path != "" {
		t.Errorf("remote default Path = %q, want empty", s.Path)
	}
}

func TestListSkipsHalfBuiltDir(t *testing.T) {
	// A dir with neither data/ nor remote.url is half-built and must not
	// be treated as an existing store.
	base := t.TempDir()
	dir := filepath.Join(base, "stores", "junk")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.yaml"), "author:\n  name: x\n")
	if Exists(base, "junk") {
		t.Error("a dir with no data/ and no remote.url must not Exist")
	}
	for _, s := range List(base) {
		if s.Name == "junk" {
			t.Error("half-built dir should be skipped by List")
		}
	}
}

func TestRenameRemoteNamedStore(t *testing.T) {
	// Renaming a remote-client store moves its config + token but must
	// NOT add a data_dir pin (it has no local data).
	base := t.TempDir()
	oldDir := filepath.Join(base, "stores", "team")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(oldDir, "config.yaml"), "remote:\n  url: https://lab.local:7420\n")
	writeFile(t, filepath.Join(oldDir, "remote.token"), "tok")

	if err := Rename(base, "team", "crew"); err != nil {
		t.Fatalf("rename remote store: %v", err)
	}
	if Exists(base, "team") {
		t.Error("old remote store should be gone")
	}
	if !Exists(base, "crew") {
		t.Error("renamed remote store should exist")
	}
	raw, err := os.ReadFile(filepath.Join(base, "stores", "crew", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "data_dir") {
		t.Errorf("remote store rename must not add a data_dir pin:\n%s", raw)
	}
	if !strings.Contains(string(raw), "lab.local:7420") {
		t.Errorf("remote.url should survive the rename:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(base, "stores", "crew", "remote.token")); err != nil {
		t.Error("remote.token should move with the store")
	}
}

func TestRenameRemoteStoreToDefaultRefused(t *testing.T) {
	// A remote-client store has no local data to move to the default
	// store; renaming it to default must refuse with a clear message,
	// not a raw os.Rename "no such file" error.
	base := t.TempDir()
	dir := filepath.Join(base, "stores", "team")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config.yaml"), "remote:\n  url: https://lab.local:7420\n")

	err := Rename(base, "team", "default")
	if err == nil || !strings.Contains(err.Error(), "no local data") {
		t.Fatalf("renaming a remote store to default should refuse clearly, got: %v", err)
	}
	// The remote store is left intact (no data lost, config untouched).
	if !Exists(base, "team") {
		t.Error("refused rename must leave the remote store intact")
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
