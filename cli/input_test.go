package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestReadFileJSON(t *testing.T) {
	// File must be in gramaton temp dir.
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "test-read.json")
	if err := os.WriteFile(path, []byte(`{"name":"test","value":42}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err != nil {
		t.Fatalf("readFileJSON: %v", err)
	}
	if target.Name != "test" || target.Value != 42 {
		t.Errorf("got %+v", target)
	}

	// File should be deleted since it's in the temp dir.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file in temp dir should be deleted after read")
	}
}

func TestReadFileJSON_RejectsOutsideTempDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct{}
	limits := config.Defaults().Limits
	err := readFileJSON(path, &target, limits)
	if err == nil {
		t.Fatal("expected error for file outside temp dir")
	}
	if got := err.Error(); got != "--file path must be inside the gramaton temp directory (use 'gramaton tempdir' to find it)" {
		t.Errorf("unexpected error: %s", got)
	}

	// File should not be deleted.
	if _, err := os.Stat(path); err != nil {
		t.Error("file outside temp dir should not be touched")
	}
}

func TestReadFileJSON_RejectsSymlinks(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	// Create a target file outside the temp dir.
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "secret.json")
	if err := os.WriteFile(targetPath, []byte(`{"secret":"value"}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	// Create a symlink inside the temp dir pointing outside.
	linkPath := filepath.Join(dir, "symlink-attack.json")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	defer os.Remove(linkPath)

	var target struct{}
	limits := config.Defaults().Limits
	err = readFileJSON(linkPath, &target, limits)
	if err == nil {
		t.Fatal("expected error for symlink escaping temp dir")
	}

	// Target file should be untouched.
	if _, err := os.Stat(targetPath); err != nil {
		t.Error("target of symlink should not be deleted")
	}
}

func TestReadFileJSON_EmptyFile(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(path)

	var target struct{}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err == nil {
		t.Error("expected error for empty file")
	}
}

func TestReadFileJSON_InvalidUTF8(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "bad-utf8.json")
	if err := os.WriteFile(path, []byte{0xFF, 0xFE, '{', '}'}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(path)

	var target struct{}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err == nil {
		t.Error("expected error for invalid UTF-8")
	}
}

func TestReadFileJSON_BOMStripping(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "bom.json")
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"ok":true}`)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct {
		OK bool `json:"ok"`
	}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err != nil {
		t.Fatalf("readFileJSON: %v", err)
	}
	if !target.OK {
		t.Error("BOM should be stripped, JSON should parse")
	}
}

func TestReadFileJSON_NoPathLeakOnError(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "bad-json.json")
	if err := os.WriteFile(path, []byte(`{not valid`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(path)

	var target struct{}
	limits := config.Defaults().Limits
	err = readFileJSON(path, &target, limits)
	if err == nil {
		t.Fatal("expected error")
	}
	// Error should say "input file", not include the full resolved path.
	errMsg := err.Error()
	if filepath.IsAbs(errMsg) {
		t.Errorf("error message should not contain absolute path: %s", errMsg)
	}
}

func TestReadInputJSON_UsesFileWhenProvided(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "input-test.json")
	if err := os.WriteFile(path, []byte(`{"v":"file"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct {
		V string `json:"v"`
	}
	limits := config.Defaults().Limits
	if err := readInputJSON(path, &target, limits); err != nil {
		t.Fatalf("readInputJSON: %v", err)
	}
	if target.V != "file" {
		t.Errorf("V = %q, want %q", target.V, "file")
	}
}
