package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brandonlattin/gramaton/config"
)

func TestReadFileJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
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

	// File outside gramaton temp dir should not be deleted.
	if _, err := os.Stat(path); err != nil {
		t.Error("file outside temp dir should not be deleted")
	}
}

func TestReadFileJSON_DeletesFromTempDir(t *testing.T) {
	dir, err := TempDir()
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}

	path := filepath.Join(dir, "delete-test.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct {
		X int `json:"x"`
	}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err != nil {
		t.Fatalf("readFileJSON: %v", err)
	}
	if target.X != 1 {
		t.Errorf("X = %d, want 1", target.X)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file in gramaton temp dir should be deleted after read")
	}
}

func TestReadFileJSON_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct{}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err == nil {
		t.Error("expected error for empty file")
	}
}

func TestReadFileJSON_InvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte{0xFF, 0xFE, '{', '}'}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var target struct{}
	limits := config.Defaults().Limits
	if err := readFileJSON(path, &target, limits); err == nil {
		t.Error("expected error for invalid UTF-8")
	}
}

func TestReadFileJSON_BOMStripping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.json")
	// UTF-8 BOM + valid JSON.
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

func TestReadInputJSON_FallsBackToStdin(t *testing.T) {
	// When filePath is empty, readInputJSON should call readStdinJSON.
	// We can't easily test stdin here without replacing os.Stdin,
	// so just verify the file path takes precedence.
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
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
