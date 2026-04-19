package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/graph"
)

// TestBackupDoesNotBlockWrites proves the phase-1-under-RLock /
// phase-2-off-lock split in api.BackupCreate. If BackupCreate held
// the engine lock through the compression step (the pre-migration
// bug), a concurrent write would block for the full duration; with
// the fix, the write lands while compression is still running.
func TestBackupDoesNotBlockWrites(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Override backup dir so we don't pollute ~/.gramaton/backups.
	backupDir := t.TempDir()
	cfg := eng.Config()
	cfg.Backup.Dir = backupDir

	// Seed enough content to make the backup take measurable time.
	// Each addRecord creates one node + one commit; 50 is enough
	// for a few ms of tar work on warm filesystems.
	for i := 0; i < 50; i++ {
		addRecord(t, eng, "backup blocker seed")
	}

	writeDone := make(chan time.Duration, 1)
	backupDone := make(chan struct{})

	// Start backup in background.
	go func() {
		_, apiErr := srv.api.BackupCreate(context.Background())
		if apiErr != nil {
			t.Errorf("BackupCreate: %v", apiErr)
		}
		close(backupDone)
	}()

	// Give the backup a moment to enter its off-lock compression
	// phase. If the old buggy pattern were in effect (RLock held
	// through backup.Create), this small delay would let the backup
	// already be holding the lock when we try to take it.
	time.Sleep(5 * time.Millisecond)

	// Try to write while backup is running. Measure how long it
	// takes to acquire the write lock + commit.
	go func() {
		start := time.Now()
		eng.Lock()
		_ = eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty("concurrent write"),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
		})
		_, _ = eng.Save("concurrent write")
		eng.Unlock()
		writeDone <- time.Since(start)
	}()

	select {
	case elapsed := <-writeDone:
		// The write landed. If it took more than 250ms, the backup
		// is blocking us -- the fix isn't working. (A backup of 50
		// small records should take <100ms; the test compiles more
		// tolerance into the bound because cold disks vary.)
		if elapsed > 250*time.Millisecond {
			t.Fatalf("concurrent write blocked for %v -- backup is holding the engine lock longer than the snapshot phase", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent write never landed -- backup appears to be holding the engine lock through compression")
	}

	<-backupDone
}

// TestBackupSnapshotConsistency verifies that the archive captures
// the state at the snapshot moment, not the state after concurrent
// writes. Flow: take a backup in-flight, do a capture during it,
// restore the backup, confirm the capture is NOT present.
func TestBackupSnapshotConsistency(t *testing.T) {
	srv, eng := setupTestServer(t)

	backupDir := t.TempDir()
	cfg := eng.Config()
	cfg.Backup.Dir = backupDir

	// Anchor record (present in the snapshot).
	anchorID := addRecord(t, eng, "present in backup snapshot")

	// Kick off backup.
	backupResult := make(chan api.BackupCreateResponse, 1)
	go func() {
		result, apiErr := srv.api.BackupCreate(context.Background())
		if apiErr != nil {
			t.Errorf("BackupCreate: %v", apiErr)
		}
		backupResult <- result
	}()

	// While backup is compressing, race in a post-snapshot commit.
	// The snapshot has already grabbed HEAD under RLock (phase 1);
	// this new commit should NOT be reachable from the archived HEAD.
	time.Sleep(5 * time.Millisecond)
	postSnapshotID := addRecord(t, eng, "captured AFTER snapshot -- must not appear in restore")

	backup := <-backupResult
	if backup.Path == "" {
		t.Fatal("backup returned empty path")
	}

	// Restore into a fresh data dir.
	restoreDir := t.TempDir()
	if err := backupPkgRestore(t, backup.Path, restoreDir); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Check HEAD in the restored dir.
	restoredHEAD, err := os.ReadFile(filepath.Join(restoreDir, "HEAD"))
	if err != nil {
		t.Fatalf("read restored HEAD: %v", err)
	}
	restoredHash := string(restoredHEAD)
	restoredHash = trimTrailingWhitespace(restoredHash)
	if restoredHash == "" {
		t.Fatal("restored HEAD is empty")
	}

	// Load the restored graph and verify only the anchor is there.
	restoredGraph := graph.New()
	store := eng.Store() // same content-store; chunks are content-addressed
	if _, err := restoredGraph.Load(store, restoredHash); err != nil {
		t.Fatalf("load restored graph: %v", err)
	}
	if _, ok := restoredGraph.GetNode(anchorID); !ok {
		t.Errorf("anchor %s missing from restored state", anchorID)
	}
	if _, ok := restoredGraph.GetNode(postSnapshotID); ok {
		t.Errorf("post-snapshot record %s leaked into restored state -- snapshot was not consistent", postSnapshotID)
	}
}

// backupPkgRestore extracts an archive into targetDir for the
// snapshot-consistency test. We can't use api.BackupRestore because
// that overwrites the running engine; we want to inspect the tarball
// contents independently.
func backupPkgRestore(t *testing.T, archivePath, targetDir string) error {
	t.Helper()
	return backup.Restore(archivePath, targetDir)
}

func trimTrailingWhitespace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// TestBranchCheckoutOffLockGraphLoad proves BranchCheckout loads
// the target graph off-lock. This is the phase-2-no-lock step. If
// the load were under lock, a concurrent read taken immediately
// after the checkout starts would stall.
func TestBranchCheckoutOffLockGraphLoad(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Seed records so the graph-load step does real work.
	for i := 0; i < 20; i++ {
		addRecord(t, eng, "checkout load target")
	}

	// Create a branch after there's content to point at.
	_, apiErr := srv.api.BranchCreate(context.Background(), api.BranchCreateRequest{Name: "worktree"})
	if apiErr != nil {
		t.Fatalf("BranchCreate: %v", apiErr)
	}

	checkoutDone := make(chan struct{})
	go func() {
		_, apiErr := srv.api.BranchCheckout(context.Background(), "worktree")
		if apiErr != nil {
			t.Errorf("BranchCheckout: %v", apiErr)
		}
		close(checkoutDone)
	}()

	// A read during the off-lock parse phase should complete
	// immediately. The only window where checkout holds the write
	// lock is phase 3 (SwapGraph + RebuildAllIndexes).
	start := time.Now()
	eng.RLock()
	_ = eng.Graph().NodeCount()
	eng.RUnlock()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("concurrent read during checkout blocked for %v", elapsed)
	}
	<-checkoutDone
}

// TestSnapshotReadFromDisk confirms ReadSnapshot captures HEAD/refs/
// FORMAT from the dataDir as expected.
func TestSnapshotReadFromDisk(t *testing.T) {
	_, eng := setupTestServer(t)
	addRecord(t, eng, "for snapshot")

	dataDir := eng.Config().DataDir
	var snap backup.Snapshot
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		snap, err = backup.ReadSnapshot(dataDir)
	}()
	wg.Wait()
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if snap.HEAD == "" {
		t.Error("HEAD should be populated")
	}
	if _, ok := snap.Refs["main"]; !ok {
		t.Errorf("main ref should be in snapshot, got %+v", snap.Refs)
	}
}
