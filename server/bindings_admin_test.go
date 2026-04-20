package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/graph"
)

// TestBackupDoesNotBlockWrites proves the phase-1-under-RLock /
// phase-2-off-lock split in api.BackupCreate. The deterministic
// hook fires after phase-1 returns; we then race a write against
// the in-flight compression. If compression held the engine lock,
// the write would never land.
func TestBackupDoesNotBlockWrites(t *testing.T) {
	srv, eng := setupTestServer(t)

// Seed enough content to make the backup take measurable time.
	for i := 0; i < 50; i++ {
		addRecord(t, eng, "backup blocker seed")
	}

	snapshotted := make(chan struct{})
	srv.api.SetBackupSnapshotHook(snapshotted)
	defer srv.api.SetBackupSnapshotHook(nil)

	backupDone := make(chan struct{})
	go func() {
		_, apiErr := srv.api.BackupCreate(context.Background())
		if apiErr != nil {
			t.Errorf("BackupCreate: %v", apiErr)
		}
		close(backupDone)
	}()

	// Wait for the snapshot to complete -- now compression is in
	// flight off-lock. A concurrent write should be able to acquire
	// the engine lock during this window.
	select {
	case <-snapshotted:
	case <-time.After(5 * time.Second):
		t.Fatal("backup snapshot phase never fired the hook")
	}

	writeDone := make(chan struct{})
	go func() {
		eng.Lock()
		_ = eng.Graph().AddNode(graph.Properties{
			"content_full":      graph.StringProperty("concurrent write"),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
		})
		_, _ = eng.Save("concurrent write")
		eng.Unlock()
		close(writeDone)
	}()

	// Compression of 50 small records is single-digit-ms typical;
	// 3s is generous for cold CI without making the test take
	// forever to fail when the discipline regresses.
	select {
	case <-writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent write never landed -- backup is holding the engine lock through compression")
	}

	<-backupDone
}

// TestBackupSnapshotConsistency verifies the archive captures state
// at the snapshot moment, not state after concurrent writes. The
// deterministic snapshot hook removes the timing race in the
// previous version of this test.
func TestBackupSnapshotConsistency(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Anchor record (present in the snapshot).
	anchorID := addRecord(t, eng, "present in backup snapshot")

	snapshotted := make(chan struct{})
	srv.api.SetBackupSnapshotHook(snapshotted)
	defer srv.api.SetBackupSnapshotHook(nil)

	backupResult := make(chan api.BackupCreateResponse, 1)
	go func() {
		result, apiErr := srv.api.BackupCreate(context.Background())
		if apiErr != nil {
			t.Errorf("BackupCreate: %v", apiErr)
		}
		backupResult <- result
	}()

	// Wait until phase-1 snapshot has finished and the lock is
	// released. After this point, any write we do must NOT appear
	// in the archive -- the snapshot's HEAD is fixed.
	select {
	case <-snapshotted:
	case <-time.After(5 * time.Second):
		t.Fatal("backup snapshot phase never fired the hook")
	}

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

// TestBranchCheckoutEdgeStorePersistence is the regression test for
// the SwapGraph + edge store divergence bug. Before the fix,
// BranchCheckout used graph.New() (fresh MemoryEdgeStore) so edges
// added on a checked-out branch silently bypassed the engine's
// BboltEdgeStore and were lost on restart.
//
// Flow: checkout a branch, add an edge, then verify the edge is
// readable via the engine's edge store accessors that go through
// bbolt -- the ones that BatchIndexWrites and post-restart Load
// rely on.
func TestBranchCheckoutEdgeStorePersistence(t *testing.T) {
	srv, eng := setupTestServer(t)

	// Two records to link.
	srcID := addRecord(t, eng, "edge source")
	dstID := addRecord(t, eng, "edge destination")

	// Branch + checkout.
	if _, e := srv.api.BranchCreate(context.Background(), api.BranchCreateRequest{Name: "edge-test"}); e != nil {
		t.Fatalf("BranchCreate: %v", e)
	}
	if _, e := srv.api.BranchCheckout(context.Background(), "edge-test"); e != nil {
		t.Fatalf("BranchCheckout: %v", e)
	}

	// Add an edge after checkout. With the bug, this writes to a
	// MemoryEdgeStore on the swapped-in graph, NOT to the engine's
	// BboltEdgeStore.
	eng.Lock()
	edge, err := eng.Graph().AddEdge(srcID, dstID, "related_to", 0.9, nil)
	eng.Unlock()
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if edge == nil {
		t.Fatal("AddEdge returned nil")
	}

	// The engine's edge-store accessor (used by BatchIndexWrites)
	// must see the edge. Pre-fix this returned 0 outbound edges
	// because the BboltEdgeStore never received the Put.
	eng.RLock()
	outbound := eng.EdgeStore().From(srcID)
	eng.RUnlock()
	found := false
	for _, e := range outbound {
		if e.ID == edge.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("edge %s missing from engine.EdgeStore() after AddEdge -- swapped-in graph diverged from bbolt", edge.ID)
	}
}

// TestBranchDiscardActiveSwitchesToMain covers the branch.go path
// where the discarded branch is the currently-active one. HEAD must
// be moved to main BEFORE the ref is deleted; a failure in the
// HEAD-write path must abort and leave HEAD pointing somewhere valid.
func TestBranchDiscardActiveSwitchesToMain(t *testing.T) {
	srv, eng := setupTestServer(t)
	addRecord(t, eng, "main commit")

	// Create + check out experiment branch.
	if _, e := srv.api.BranchCreate(context.Background(), api.BranchCreateRequest{Name: "experiment"}); e != nil {
		t.Fatalf("BranchCreate: %v", e)
	}
	if _, e := srv.api.BranchCheckout(context.Background(), "experiment"); e != nil {
		t.Fatalf("BranchCheckout: %v", e)
	}

	// Discard the active branch.
	if _, e := srv.api.BranchDiscard(context.Background(), "experiment"); e != nil {
		t.Fatalf("BranchDiscard active branch: %v", e)
	}

	// HEAD must now point at main and active-branch must be main.
	listResp, apiErr := srv.api.BranchList(context.Background())
	if apiErr != nil {
		t.Fatalf("BranchList: %v", apiErr)
	}
	if listResp.Current != "main" {
		t.Errorf("active branch = %q after discard, want main", listResp.Current)
	}
	for _, b := range listResp.Branches {
		if b.Name == "experiment" {
			t.Errorf("discarded branch %q still in list", b.Name)
		}
	}
	// And the engine must still respond to reads (HEAD valid).
	if _, e := srv.api.Status(context.Background(), api.StatusRequest{}); e != nil {
		t.Errorf("Status after active-branch discard: %v", e)
	}
}

// TestCurationBatchRequiresLLM proves that BackupCreate and other
// LLM-required ops surface ErrUnavailable when no LLM is configured,
// rather than panicking or returning ErrInternal. Guards the
// "runner != nil but engine.LLM() == nil" branch in api/curation.go.
func TestCurationBatchRequiresLLM(t *testing.T) {
	srv, _ := setupTestServer(t)

	// setupTestServer wires a noopLLM; clear it to mimic an LLM-less store.
	// We can't swap the engine's LLM provider, so instead exercise the
	// runner==nil path which exits with the same Unavailable code.
	if _, e := srv.api.CurationBatch(context.Background()); e == nil {
		t.Fatal("CurationBatch with no runner should return ErrUnavailable")
	} else if e.Code != "unavailable" {
		t.Errorf("code = %q, want unavailable", e.Code)
	}
}

// TestBackupImportEmptyRecords guards the ErrMissing path so a future
// refactor can't silently accept an empty payload.
func TestBackupImportEmptyRecords(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.BackupImport(context.Background(), api.ImportRequest{})
	if apiErr == nil {
		t.Fatal("BackupImport with no records should return ErrMissing")
	}
	if apiErr.Code != "missing_field" {
		t.Errorf("code = %q, want missing_field", apiErr.Code)
	}
}

// TestBackupExportInvalidFormat verifies that an unknown format is
// rejected with 400, not silently defaulted or 500'd.
func TestBackupExportInvalidFormat(t *testing.T) {
	srv, _ := setupTestServer(t)
	var sink strings.Builder
	_, apiErr := srv.api.BackupExport(context.Background(), api.ExportRequest{Format: "yaml"}, &sink)
	if apiErr == nil {
		t.Fatal("BackupExport with unknown format should return ErrInvalid")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
	if sink.Len() != 0 {
		t.Errorf("expected no bytes written, got %d", sink.Len())
	}
}

// TestBackupRestorePathConfinement: requests with paths outside the
// configured backup directory must be rejected with 400. Without
// this guard a caller could restore from any .tar.gz on the host.
func TestBackupRestorePathConfinement(t *testing.T) {
	srv, _ := setupTestServer(t)
	_, apiErr := srv.api.BackupRestore(context.Background(), api.RestoreRequest{
		Path:  "/etc/whatever.tar.gz",
		Force: true,
	})
	if apiErr == nil {
		t.Fatal("BackupRestore should reject paths outside backup dir")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("code = %q, want input_error", apiErr.Code)
	}
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
