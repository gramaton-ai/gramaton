package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateAndRestore(t *testing.T) {
	// Set up a fake data directory with HEAD, BRANCH, refs, and chunks.
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Create store structure.
	writeFile(t, filepath.Join(dataDir, "HEAD"), "abc123hash")
	writeFile(t, filepath.Join(dataDir, "BRANCH"), "main")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "abc123hash")

	// Create a chunk directory.
	chunkDir := filepath.Join(dataDir, "ab")
	os.MkdirAll(chunkDir, 0o700)
	writeFile(t, filepath.Join(chunkDir, "abc123hash"), `{"data":"test"}`)

	// Create backup.
	archivePath, err := Create(dataDir, "", backupDir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify archive exists.
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("archive is empty")
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %o", info.Mode().Perm())
	}

	// Verify filename format.
	base := filepath.Base(archivePath)
	if !strings.HasPrefix(base, "gramaton-backup-") {
		t.Fatalf("unexpected filename: %s", base)
	}
	if !strings.HasSuffix(base, ".tar.gz") {
		t.Fatalf("unexpected extension: %s", base)
	}

	// Restore to a new directory.
	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify restored files.
	assertFileContent(t, filepath.Join(restoreDir, "HEAD"), "abc123hash")
	assertFileContent(t, filepath.Join(restoreDir, "BRANCH"), "main")
	assertFileContent(t, filepath.Join(restoreDir, "refs", "main"), "abc123hash")
	assertFileContent(t, filepath.Join(restoreDir, "ab", "abc123hash"), `{"data":"test"}`)
}

func TestRestoreInvalidArchive(t *testing.T) {
	dataDir := t.TempDir()

	// Create a tar.gz without HEAD file.
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	f, _ := os.Create(archivePath)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "data/some-file", Size: 4, Mode: 0o600})
	tw.Write([]byte("test"))
	tw.Close()
	gz.Close()
	f.Close()

	err := Restore(archivePath, dataDir)
	if err == nil || !strings.Contains(err.Error(), "no HEAD") {
		t.Fatalf("expected 'no HEAD' error, got: %v", err)
	}
}

// TestRestoreFailureLeavesDataDirIntact is the regression test for
// P0-01: before the staging-dir rewrite, Restore wiped dataDir
// before validating archive contents, so a malformed archive (no
// HEAD, path traversal, etc.) destroyed the user's existing data.
// After the rewrite, dataDir must be untouched on any failure.
func TestRestoreFailureLeavesDataDirIntact(t *testing.T) {
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "store")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Seed dataDir with content that MUST survive a failed restore.
	writeFile(t, filepath.Join(dataDir, "HEAD"), "original-hash")
	writeFile(t, filepath.Join(dataDir, "BRANCH"), "main")
	if err := os.MkdirAll(filepath.Join(dataDir, "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dataDir, "ab", "abc123"), `{"original":true}`)

	// Failure mode 1: archive with no HEAD entry.
	noHeadArchive := filepath.Join(t.TempDir(), "no-head.tar.gz")
	f, _ := os.Create(noHeadArchive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "data/some-file", Size: 4, Mode: 0o600})
	tw.Write([]byte("test"))
	tw.Close()
	gz.Close()
	f.Close()

	if err := Restore(noHeadArchive, dataDir); err == nil {
		t.Fatal("expected error for archive with no HEAD")
	}
	assertFileContent(t, filepath.Join(dataDir, "HEAD"), "original-hash")
	assertFileContent(t, filepath.Join(dataDir, "BRANCH"), "main")
	assertFileContent(t, filepath.Join(dataDir, "ab", "abc123"), `{"original":true}`)

	// Failure mode 2: archive with a path-traversal entry. Must
	// also leave dataDir untouched.
	evilArchive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ = os.Create(evilArchive)
	gz = gzip.NewWriter(f)
	tw = tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "data/HEAD", Size: 3, Mode: 0o600})
	tw.Write([]byte("new"))
	tw.WriteHeader(&tar.Header{Name: "data/../../../etc/evil", Size: 4, Mode: 0o600})
	tw.Write([]byte("pwnd"))
	tw.Close()
	gz.Close()
	f.Close()

	if err := Restore(evilArchive, dataDir); err == nil {
		t.Fatal("expected error for archive with path traversal")
	}
	assertFileContent(t, filepath.Join(dataDir, "HEAD"), "original-hash")
	assertFileContent(t, filepath.Join(dataDir, "ab", "abc123"), `{"original":true}`)

	// Verify no leftover staging directories in parent (cleanup
	// happened on each failure path).
	siblings, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range siblings {
		if strings.HasPrefix(s.Name(), "store.restore-staging-") ||
			strings.HasPrefix(s.Name(), "store.replaced-") {
			t.Errorf("leftover restore artefact in parent dir: %s", s.Name())
		}
	}
}

func TestRestorePathTraversal(t *testing.T) {
	dataDir := t.TempDir()

	// Create a tar.gz with path traversal attempt.
	archivePath := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(archivePath)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	// HEAD for validation.
	tw.WriteHeader(&tar.Header{Name: "data/HEAD", Size: 3, Mode: 0o600})
	tw.Write([]byte("abc"))
	// Path traversal.
	tw.WriteHeader(&tar.Header{Name: "data/../../../etc/evil", Size: 4, Mode: 0o600})
	tw.Write([]byte("pwnd"))
	tw.Close()
	gz.Close()
	f.Close()

	err := Restore(archivePath, dataDir)
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected path traversal error, got: %v", err)
	}
}

func TestRestorePathTraversalPrefixFalsePositive(t *testing.T) {
	// Ensure a directory name that is a prefix of the data dir
	// doesn't bypass the traversal check. E.g., dataDir="/tmp/foo"
	// should not allow writes to "/tmp/foobar/evil".
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "store")
	os.MkdirAll(dataDir, 0o700)

	archivePath := filepath.Join(t.TempDir(), "prefix.tar.gz")
	f, _ := os.Create(archivePath)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "data/HEAD", Size: 3, Mode: 0o600})
	tw.Write([]byte("abc"))
	// This name, after filepath.Join(dataDir, name), would resolve
	// to a sibling directory if prefix check is naive.
	tw.WriteHeader(&tar.Header{Name: "data/../storebar/evil", Size: 4, Mode: 0o600})
	tw.Write([]byte("pwnd"))
	tw.Close()
	gz.Close()
	f.Close()

	err := Restore(archivePath, dataDir)
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected path traversal error for prefix attack, got: %v", err)
	}
}

func TestRestoreClearsExistingData(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Create initial data.
	writeFile(t, filepath.Join(dataDir, "HEAD"), "old-hash")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "old-hash")
	writeFile(t, filepath.Join(dataDir, "old-file.txt"), "should be deleted")

	// Create backup with different data.
	backupDataDir := t.TempDir()
	writeFile(t, filepath.Join(backupDataDir, "HEAD"), "new-hash")
	os.MkdirAll(filepath.Join(backupDataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(backupDataDir, "refs", "main"), "new-hash")

	archivePath, err := Create(backupDataDir, "", backupDir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Restore.
	if err := Restore(archivePath, dataDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Old file should be gone.
	if _, err := os.Stat(filepath.Join(dataDir, "old-file.txt")); !os.IsNotExist(err) {
		t.Fatal("old file should have been deleted during restore")
	}

	// New data should be present.
	assertFileContent(t, filepath.Join(dataDir, "HEAD"), "new-hash")
}

func TestApplyRetention(t *testing.T) {
	dir := t.TempDir()

	// Create 4 backup files with staggered times.
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "gramaton-backup-2026-04-0"+
			string(rune('1'+i))+"T00-00-00Z.tar.gz")
		writeFile(t, name, "backup-data")
		// Ensure different mod times.
		mtime := time.Now().Add(time.Duration(i) * time.Minute)
		os.Chtimes(name, mtime, mtime)
	}

	// Retain 2.
	deleted, err := ApplyRetention(dir, 2)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deleted, got %d", len(deleted))
	}

	// Verify 2 remain.
	remaining, _ := filepath.Glob(filepath.Join(dir, "gramaton-backup-*.tar.gz"))
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(remaining))
	}
}

func TestApplyRetentionUnlimited(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "gramaton-backup-2026-04-0"+
			string(rune('1'+i))+"T00-00-00Z.tar.gz")
		writeFile(t, name, "data")
	}

	deleted, err := ApplyRetention(dir, 0)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("unlimited retention should not delete, got %d deleted", len(deleted))
	}
}

func TestStripAPIKeys(t *testing.T) {
	input := `llm:
  provider: anthropic
  model: claude-sonnet-4-6
  api_key_env: sk-ant-secret-key-here
embedding:
  provider: ollama
  api_key_env: MY_EMBEDDING_KEY
  aws_access_key_id_env: AKIAIOSFODNN7EXAMPLE
  aws_secret_access_key_env: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
`

	result, err := StripAPIKeys([]byte(input))
	if err != nil {
		t.Fatalf("StripAPIKeys: %v", err)
	}

	s := string(result)
	if strings.Contains(s, "sk-ant") {
		t.Fatal("API key not stripped from llm section")
	}
	if strings.Contains(s, "MY_EMBEDDING_KEY") {
		t.Fatal("API key env not stripped from embedding section")
	}
	if strings.Contains(s, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("AWS access key not stripped")
	}
	if strings.Contains(s, "wJalrXUtnFEMI") {
		t.Fatal("AWS secret key not stripped")
	}
	if !strings.Contains(s, "anthropic") {
		t.Fatal("non-sensitive fields should be preserved")
	}
	if !strings.Contains(s, "claude-sonnet") {
		t.Fatal("model should be preserved")
	}
}

// TestStripAPIKeysWhitelistGaps is the regression test for P1-03:
// the previous blacklist missed several fields that leak secrets or
// infrastructure. The whitelist rewrite must drop ALL of them.
func TestStripAPIKeysWhitelistGaps(t *testing.T) {
	input := `llm:
  provider: anthropic
  model: claude-sonnet-4-6
  api_key: sk-ant-INLINE-secret
  api_key_file: /home/alice/.gramaton/anthropic.key
  base_url: https://internal-proxy.acme.local/anthropic
  aws_profile: production-readonly
  region: us-east-1
embedding:
  provider: bedrock
  model: cohere.embed-english-v3
  dimension: 1024
  api_key_file: /home/alice/.gramaton/embed.key
  base_url: https://my-internal-llm.acme.local
  aws_profile: embeddings-account
`
	result, err := StripAPIKeys([]byte(input))
	if err != nil {
		t.Fatalf("StripAPIKeys: %v", err)
	}
	s := string(result)

	// All sensitive fields gone.
	for _, secret := range []string{
		"sk-ant-INLINE-secret",       // inline key
		"/home/alice/.gramaton",      // filesystem layout (api_key_file)
		"internal-proxy.acme.local",  // base_url leak
		"my-internal-llm.acme.local", // base_url leak
		"production-readonly",        // aws_profile leak
		"embeddings-account",         // aws_profile leak
	} {
		if strings.Contains(s, secret) {
			t.Errorf("StripAPIKeys leaked %q in output:\n%s", secret, s)
		}
	}

	// Whitelist preserved fields.
	for _, kept := range []string{
		"anthropic",
		"claude-sonnet-4-6",
		"bedrock",
		"cohere.embed-english-v3",
		"dimension",
		"us-east-1", // region is on the safe list
	} {
		if !strings.Contains(s, kept) {
			t.Errorf("StripAPIKeys dropped expected field %q from output:\n%s", kept, s)
		}
	}
}

func TestExcludedFilesSnapshot(t *testing.T) {
	tests := []struct {
		rel      string
		excluded bool
	}{
		// Transients (excluded).
		{"gramaton.log", true},
		{"gramaton.log.1.gz", true},
		{"server.json", true},
		{".gramaton-tmp-123", true},
		{".chunk-abc", true},
		// Snapshot-injected (excluded from walk -- re-added from memory).
		{"HEAD", true},
		{"FORMAT", true},
		{"refs", true},
		{"refs/main", true},
		{"refs/experiment", true},
		// Derived index state (excluded -- rebuilt on restore).
		{"indexes.db", true},
		{"vec.flat", true},
		// Content-addressed chunks (kept).
		{"ab/abc123hash", false},
		{"00/000dd47654af104be2c54ac4e9028749e0fc114fb9ade0e8df88bd4e354dd909", false},
	}

	for _, tt := range tests {
		if got := shouldExcludeSnapshot(tt.rel, nil); got != tt.excluded {
			t.Errorf("shouldExcludeSnapshot(%q) = %v, want %v", tt.rel, got, tt.excluded)
		}
	}
}

func TestIsBackupArchive(t *testing.T) {
	tests := []struct {
		path   string
		expect bool
	}{
		{"/backups/gramaton-backup-2026-04-05T00-00-00Z.tar.gz", true},
		{"/backups/gramaton-backup-2026.tar.gz", true},
		{"/backups/other-file.tar.gz", false},
		{"/backups/gramaton-backup-2026.zip", false},
		{"gramaton-backup-test.tar.gz", true},
	}

	for _, tt := range tests {
		if got := IsBackupArchive(tt.path); got != tt.expect {
			t.Errorf("IsBackupArchive(%q) = %v, want %v", tt.path, got, tt.expect)
		}
	}
}

func TestCreateWithConfig(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()
	cfgDir := t.TempDir()

	writeFile(t, filepath.Join(dataDir, "HEAD"), "hash")
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	writeFile(t, cfgPath, `llm:
  provider: anthropic
  api_key_env: sk-ant-secret
embedding:
  provider: ollama
`)

	archivePath, err := Create(dataDir, cfgPath, backupDir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Read config from archive and verify key is stripped.
	f, _ := os.Open(archivePath)
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	defer gz.Close()
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			t.Fatal("config.yaml not found in archive")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "config.yaml" {
			data, _ := io.ReadAll(tr)
			if strings.Contains(string(data), "sk-ant") {
				t.Fatal("API key leaked into backup archive")
			}
			if !strings.Contains(string(data), "anthropic") {
				t.Fatal("provider should be preserved")
			}
			break
		}
	}
}

func TestCreateWithStoreName(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "HEAD"), "hash")

	archivePath, err := Create(dataDir, "", backupDir, "work")
	if err != nil {
		t.Fatalf("Create with store name: %v", err)
	}

	base := filepath.Base(archivePath)
	if !strings.HasPrefix(base, "gramaton-backup-work-") {
		t.Fatalf("expected store name in filename, got: %s", base)
	}
	if !strings.HasSuffix(base, ".tar.gz") {
		t.Fatalf("expected .tar.gz suffix, got: %s", base)
	}
}

func TestCreateWithoutStoreName(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "HEAD"), "hash")

	archivePath, err := Create(dataDir, "", backupDir)
	if err != nil {
		t.Fatalf("Create without store name: %v", err)
	}

	base := filepath.Base(archivePath)
	// Should NOT contain a store name segment.
	parts := strings.SplitN(base, "-", 4)
	// Format: gramaton-backup-<timestamp>.tar.gz
	// With store name: gramaton-backup-<store>-<timestamp>.tar.gz
	if !strings.HasPrefix(base, "gramaton-backup-2") {
		t.Fatalf("expected timestamp after prefix, got: %s", base)
	}
	_ = parts
}

func TestCreateWithEmptyStoreName(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "HEAD"), "hash")

	// Empty store name should behave like no store name.
	archivePath, err := Create(dataDir, "", backupDir, "")
	if err != nil {
		t.Fatalf("Create with empty store name: %v", err)
	}

	base := filepath.Base(archivePath)
	if !strings.HasPrefix(base, "gramaton-backup-2") {
		t.Fatalf("empty store name should not appear in filename, got: %s", base)
	}
}

// --- Helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) != expected {
		t.Fatalf("%s: got %q, want %q", path, strings.TrimSpace(string(data)), expected)
	}
}
