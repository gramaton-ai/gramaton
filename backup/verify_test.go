package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureStore writes the minimal on-disk shape Create snapshots.
func fixtureStore(t *testing.T, head string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "FORMAT"), []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "refs", "main"), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestVerifyArchiveReadsSnapshotHead pins the prune backup gate's
// proof: verification OPENS the archive and returns the snapshot
// HEAD, and a non-archive fails instead of passing on existence.
func TestVerifyArchiveReadsSnapshotHead(t *testing.T) {
	head := "abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
	dataDir := fixtureStore(t, head)
	outDir := t.TempDir()

	archive, err := Create(dataDir, "", outDir, "teststore")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := VerifyArchive(archive)
	if err != nil {
		t.Fatalf("VerifyArchive: %v", err)
	}
	if info.HEAD != head {
		t.Fatalf("archived HEAD = %q, want %q", info.HEAD, head)
	}
	if info.Format != "2" {
		t.Fatalf("archived FORMAT = %q, want 2", info.Format)
	}

	bogus := filepath.Join(outDir, "gramaton-backup-teststore-bogus.tar.gz")
	if err := os.WriteFile(bogus, []byte("not a tar.gz"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(bogus); err == nil {
		t.Fatal("garbage archive verified")
	}
}

// TestNewestArchiveForMatchesStoreName pins store identity: the gate
// must pick this store's newest archive, never another store's.
func TestNewestArchiveForMatchesStoreName(t *testing.T) {
	dataDir := fixtureStore(t, "aaaa")
	outDir := t.TempDir()

	if _, err := Create(dataDir, "", outDir, "other"); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	want, err := Create(dataDir, "", outDir, "teststore")
	if err != nil {
		t.Fatalf("Create teststore: %v", err)
	}
	got, err := NewestArchiveFor(outDir, "teststore")
	if err != nil {
		t.Fatalf("NewestArchiveFor: %v", err)
	}
	if got != want {
		t.Fatalf("picked %q, want %q", got, want)
	}
	if got, _ := NewestArchiveFor(outDir, "absent-store"); got != "" {
		t.Fatalf("picked %q for a store with no archives", got)
	}
}
