package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key\n"), 0o600)

	key := ResolveKey(keyPath, "")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected trimmed key from file, got %q", key)
	}
}

func TestResolveKeyFromEnv(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-env-key")
	key := ResolveKey("", "TEST_SECRET_KEY")
	if key != "sk-ant-env-key" {
		t.Fatalf("expected key from env, got %q", key)
	}
}

func TestResolveKeyDirect(t *testing.T) {
	key := ResolveKey("", "sk-ant-direct")
	if key != "sk-ant-direct" {
		t.Fatalf("expected direct key, got %q", key)
	}
}

func TestResolveKeyFileTakesPrecedence(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-env-key")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api.key")
	os.WriteFile(keyPath, []byte("sk-ant-file-key"), 0o600)

	key := ResolveKey(keyPath, "TEST_SECRET_KEY")
	if key != "sk-ant-file-key" {
		t.Fatalf("file should take precedence, got %q", key)
	}
}

func TestResolveKeyEmpty(t *testing.T) {
	key := ResolveKey("", "")
	if key != "" {
		t.Fatalf("expected empty, got %q", key)
	}
}

func TestResolveKeyMissingFile(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-fallback")
	key := ResolveKey("/nonexistent/path", "TEST_SECRET_KEY")
	if key != "sk-ant-fallback" {
		t.Fatalf("should fall back to env, got %q", key)
	}
}

func TestResolveKeyEmptyFile(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-fallback")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "empty.key")
	os.WriteFile(keyPath, []byte("  \n"), 0o600)

	key := ResolveKey(keyPath, "TEST_SECRET_KEY")
	if key != "sk-ant-fallback" {
		t.Fatalf("empty file should fall back to env, got %q", key)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got := expandHome("~/test/path")
	want := filepath.Join(home, "test/path")
	if got != want {
		t.Fatalf("expandHome(~/test/path) = %q, want %q", got, want)
	}
}

func TestExpandHomeNoTilde(t *testing.T) {
	got := expandHome("/absolute/path")
	if got != "/absolute/path" {
		t.Fatalf("expandHome should not modify absolute path, got %q", got)
	}
}
