package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestBackupIncludesJobs — backup with a populated jobs.db must
// include it in the tarball, and the included file must be a valid
// bbolt database (openable, contains the records we wrote).
func TestBackupIncludesJobs(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Create a jobs.db with one entry.
	jobsPath := filepath.Join(dataDir, "jobs.db")
	db, err := bolt.Open(jobsPath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		if err != nil {
			return err
		}
		return b.Put([]byte("test-id"), []byte(`{"id":"test-id","status":"completed"}`))
	}); err != nil {
		t.Fatal(err)
	}

	// Minimal store fixtures (HEAD/refs needed for snapshot).
	writeFile(t, filepath.Join(dataDir, "HEAD"), "deadbeef")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "deadbeef")

	// Take the snapshot WITH the live JobsDB handle (mimics the
	// production path where api/backup.go passes engine.JobStore().DB()).
	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.JobsDB = db

	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Open the tarball; assert jobs.db is present.
	if !tarballContains(t, archivePath, "data/jobs.db") {
		t.Fatal("jobs.db not present in tarball")
	}

	// Restore and verify the restored jobs.db opens cleanly and
	// contains our record (proves the bbolt-native snapshot was
	// coherent, not a torn page-copy).
	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoredDB, err := bolt.Open(filepath.Join(restoreDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatalf("restored jobs.db not openable: %v", err)
	}
	defer restoredDB.Close()

	var got []byte
	if err := restoredDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("jobs"))
		if b == nil {
			return nil
		}
		got = b.Get([]byte("test-id"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"id":"test-id"`) {
		t.Errorf("restored jobs.db lost the test record; got %q", got)
	}
}

// TestBackupJobsConcurrentWrite — runner does rapid writes to
// jobs.db while backup runs; the tarball's jobs.db must still
// open without "checksum mismatch" / torn-page errors.
func TestBackupJobsConcurrentWrite(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	jobsPath := filepath.Join(dataDir, "jobs.db")
	db, err := bolt.Open(jobsPath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Seed the bucket.
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dataDir, "HEAD"), "live")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "live")

	// Start a goroutine doing rapid writes to jobs.db.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("jobs"))
				key := []byte{byte(i % 256)}
				return b.Put(key, []byte(`{"writing":true}`))
			})
			i++
		}
	}()

	// Take snapshot (with live handle) while writes are in flight.
	time.Sleep(10 * time.Millisecond) // let writer warm up
	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.JobsDB = db
	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}

	close(stop)
	wg.Wait()

	// Restore and verify the snapshot opens without errors.
	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restoredDB, err := bolt.Open(filepath.Join(restoreDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatalf("restored jobs.db not openable (torn snapshot): %v", err)
	}
	if err := restoredDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("jobs"))
		if b == nil {
			return nil // empty is fine
		}
		// Walk to verify the bucket is internally consistent.
		return b.ForEach(func(_, _ []byte) error { return nil })
	}); err != nil {
		t.Errorf("restored jobs.db internally inconsistent: %v", err)
	}
	_ = restoredDB.Close()
}

// TestBackupNoJobsDBFallback — when snap.JobsDB is nil and
// jobs.db exists on disk, the walker falls back to os.ReadFile.
// Used for offline backup tools that don't have a live engine.
func TestBackupNoJobsDBFallback(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// jobs.db exists on disk but no live handle.
	db, err := bolt.Open(filepath.Join(dataDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		if err != nil {
			return err
		}
		return b.Put([]byte("offline"), []byte(`{"status":"completed"}`))
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close() // simulate engine shutdown

	writeFile(t, filepath.Join(dataDir, "HEAD"), "offline")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "offline")

	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// snap.JobsDB stays nil — caller didn't pass a live handle.

	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if !tarballContains(t, archivePath, "data/jobs.db") {
		t.Error("jobs.db not present in tarball (fallback path)")
	}
}

// TestRestoreRestoresJobs — backup with a completed job; restore
// to a fresh dir; the restored jobs.db is openable and contains
// the original job. (TestBackupIncludesJobs covered this from the
// file layer; this version walks via Get.)
func TestRestoreRestoresJobs(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	jobsPath := filepath.Join(dataDir, "jobs.db")
	db, err := bolt.Open(jobsPath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		if err != nil {
			return err
		}
		return b.Put([]byte("completed-id"),
			[]byte(`{"id":"completed-id","kind":"capture_batch","status":"completed","completed_at":"2026-05-03T00:00:00Z"}`))
	}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dataDir, "HEAD"), "abc")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "abc")

	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.JobsDB = db

	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatal(err)
	}

	// Open the restored jobs.db and verify the record is queryable.
	rdb, err := bolt.Open(filepath.Join(restoreDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var got []byte
	if err := rdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("jobs"))
		got = b.Get([]byte("completed-id"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"status":"completed"`) {
		t.Errorf("restored job lost its content: %q", got)
	}
}

// TestRestoreRunningJob — backup taken while a job is in-flight
// (status=running). Restore to a fresh dir. The restored jobs.db
// holds the running job; an engine init on this dir would flip it
// to failed/server_restart via recoverInFlightJobs. This test
// validates the file-layer property; the engine-side behavior is
// covered separately (TestEngineJobStoreRestartRecovery).
func TestRestoreRunningJob(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	db, err := bolt.Open(filepath.Join(dataDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		if err != nil {
			return err
		}
		return b.Put([]byte("in-flight"),
			[]byte(`{"id":"in-flight","kind":"capture_batch","status":"running","started_at":"2026-05-03T00:00:00Z"}`))
	}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dataDir, "HEAD"), "abc")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "abc")

	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.JobsDB = db

	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatal(err)
	}

	// File-layer check: restored jobs.db has the running entry,
	// proving the snapshot captured an in-flight state cleanly
	// without truncation/corruption.
	rdb, err := bolt.Open(filepath.Join(restoreDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var found bool
	if err := rdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("jobs"))
		v := b.Get([]byte("in-flight"))
		found = strings.Contains(string(v), `"status":"running"`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("restored jobs.db missing the in-flight running job")
	}
}

// TestBackupExcludesNestedJobsDB — chunk file at chunk-dir/jobs.db
// MUST NOT trigger the bbolt-snapshot path (would fail; not a bbolt
// file). Walker special-case is `rel == "jobs.db"` exact match;
// nested paths fall through to os.ReadFile.
func TestBackupExcludesNestedJobsDB(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Real top-level jobs.db.
	db, err := bolt.Open(filepath.Join(dataDir, "jobs.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("jobs"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Nested file confusingly named jobs.db — should be treated as
	// an opaque chunk file.
	nestedDir := filepath.Join(dataDir, "ab")
	os.MkdirAll(nestedDir, 0o700)
	writeFile(t, filepath.Join(nestedDir, "jobs.db"), "not a bbolt file")

	writeFile(t, filepath.Join(dataDir, "HEAD"), "abc")
	os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700)
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "abc")

	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.JobsDB = db

	// CreateSnapshot must NOT panic / fail on the nested file
	// even though it has the same basename.
	if _, err := CreateSnapshot(snap, dataDir, "", backupDir); err != nil {
		t.Fatalf("CreateSnapshot rejected nested-file: %v", err)
	}
	_ = db.Close()
}

// tarballContains scans the archive's tar entries for the given
// archive path. Returns true on first match.
func tarballContains(t *testing.T, archivePath, want string) bool {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == want {
			return true
		}
	}
}
