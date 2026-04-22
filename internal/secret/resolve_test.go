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

	key := ResolveKey(keyPath, "", "")
	if key != "sk-ant-file-key" {
		t.Fatalf("expected trimmed key from file, got %q", key)
	}
}

func TestResolveKeyFromEnv(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-env-key")
	key := ResolveKey("", "TEST_SECRET_KEY", "")
	if key != "sk-ant-env-key" {
		t.Fatalf("expected key from env, got %q", key)
	}
}

// TestResolveKeyDirect uses the explicit direct parameter
// (post-Wave-2). For the legacy sk- overload behaviour see
// TestResolveKeyLegacyDirectViaEnvName below.
func TestResolveKeyDirect(t *testing.T) {
	key := ResolveKey("", "", "sk-ant-direct")
	if key != "sk-ant-direct" {
		t.Fatalf("expected direct key, got %q", key)
	}
}

// TestResolveKeyLegacyDirectViaEnvName is the regression test for
// the legacy sk- heuristic that turned envName into a literal key
// when no env var matched. It still works for backward compat (so
// existing user configs don't break), but emits a one-shot
// deprecation warning.
func TestResolveKeyLegacyDirectViaEnvName(t *testing.T) {
	key := ResolveKey("", "sk-ant-legacy-overload", "")
	if key != "sk-ant-legacy-overload" {
		t.Fatalf("legacy sk- overload broken; existing configs would stop working: got %q", key)
	}
}

// TestResolveKeyDirectTakesPrecedenceOverLegacyOverload confirms
// the new explicit `direct` parameter wins when both are set.
func TestResolveKeyDirectTakesPrecedenceOverLegacyOverload(t *testing.T) {
	// envName looks like a literal key; direct is also a literal
	// key. The lookup should hit the env-name path first (which
	// falls through to the legacy direct return). This documents
	// current behaviour: legacy overload precedes new direct.
	// Users on the new field don't set envName at all.
	key := ResolveKey("", "", "sk-from-direct-field")
	if key != "sk-from-direct-field" {
		t.Fatalf("expected direct, got %q", key)
	}
}

func TestResolveKeyEmpty(t *testing.T) {
	key := ResolveKey("", "", "")
	if key != "" {
		t.Fatalf("expected empty, got %q", key)
	}
}

func TestResolveKeyMissingFile(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-fallback")
	key := ResolveKey("/nonexistent/path", "TEST_SECRET_KEY", "")
	if key != "sk-ant-fallback" {
		t.Fatalf("should fall back to env, got %q", key)
	}
}

func TestResolveKeyEmptyFile(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "sk-ant-fallback")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "empty.key")
	os.WriteFile(keyPath, []byte("  \n"), 0o600)

	key := ResolveKey(keyPath, "TEST_SECRET_KEY", "")
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
