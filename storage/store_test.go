package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "chunks"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// --- Hash ---

func TestHashDeterministic(t *testing.T) {
	data := []byte("hello world")
	h1 := Hash(data)
	h2 := Hash(data)
	if h1 != h2 {
		t.Fatalf("Hash not deterministic: %q != %q", h1, h2)
	}
}

func TestHashDifferentData(t *testing.T) {
	h1 := Hash([]byte("hello"))
	h2 := Hash([]byte("world"))
	if h1 == h2 {
		t.Fatal("different data produced same hash")
	}
}

func TestHashEmpty(t *testing.T) {
	h := Hash([]byte{})
	if len(h) != 64 {
		t.Fatalf("expected 64-char hex hash, got %d chars", len(h))
	}
}

func TestHashKnownValue(t *testing.T) {
	// SHA-256 of empty string is well-known.
	h := Hash([]byte{})
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if h != want {
		t.Fatalf("expected %q, got %q", want, h)
	}
}

func TestHashLength(t *testing.T) {
	h := Hash([]byte("test"))
	if len(h) != 64 {
		t.Fatalf("expected 64-char hex hash, got %d chars", len(h))
	}
}

// --- Write + Read round-trip ---

func TestWriteReadRoundTrip(t *testing.T) {
	s := tempStore(t)
	data := []byte("We chose Kafka over RabbitMQ for the event pipeline.")

	hash, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if hash != Hash(data) {
		t.Fatalf("Write returned wrong hash: got %q, want %q", hash, Hash(data))
	}

	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Read returned wrong data: got %q, want %q", got, data)
	}
}

func TestWriteReadEmpty(t *testing.T) {
	s := tempStore(t)
	hash, err := s.Write([]byte{})
	if err != nil {
		t.Fatalf("Write empty: %v", err)
	}

	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty data, got %d bytes", len(got))
	}
}

func TestWriteReadLargeData(t *testing.T) {
	s := tempStore(t)
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	hash, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("large data round-trip mismatch")
	}
}

func TestWriteReadBinaryData(t *testing.T) {
	s := tempStore(t)
	data := []byte{0x00, 0xFF, 0x01, 0xFE, 0x00, 0x00}

	hash, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("binary data round-trip mismatch")
	}
}

// --- Deduplication ---

func TestWriteDeduplication(t *testing.T) {
	s := tempStore(t)
	data := []byte("deduplicate me")

	h1, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}

	h2, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	if h1 != h2 {
		t.Fatal("duplicate writes returned different hashes")
	}

	// Verify only one file exists.
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, h := range list {
		if h == h1 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 chunk, found %d", count)
	}
}

// --- Has ---

func TestHasExists(t *testing.T) {
	s := tempStore(t)
	hash, _ := s.Write([]byte("exists"))
	if !s.Has(hash) {
		t.Fatal("Has returned false for existing chunk")
	}
}

func TestHasNotExists(t *testing.T) {
	s := tempStore(t)
	fakeHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if s.Has(fakeHash) {
		t.Fatal("Has returned true for non-existent chunk")
	}
}

// --- Read errors ---

func TestReadNotFound(t *testing.T) {
	s := tempStore(t)
	fakeHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := s.Read(fakeHash)
	if err == nil {
		t.Fatal("expected error for missing chunk")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- Delete ---

func TestDelete(t *testing.T) {
	s := tempStore(t)
	hash, _ := s.Write([]byte("delete me"))

	if !s.Has(hash) {
		t.Fatal("chunk should exist before delete")
	}

	if err := s.Delete(hash); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if s.Has(hash) {
		t.Fatal("chunk should not exist after delete")
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := tempStore(t)
	fakeHash := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	err := s.Delete(fakeHash)
	if err == nil {
		t.Fatal("expected error for deleting missing chunk")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteThenRead(t *testing.T) {
	s := tempStore(t)
	hash, _ := s.Write([]byte("temporary"))
	_ = s.Delete(hash)

	_, err := s.Read(hash)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// --- List ---

func TestListEmpty(t *testing.T) {
	s := tempStore(t)
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 chunks, got %d", len(list))
	}
}

func TestListMultiple(t *testing.T) {
	s := tempStore(t)
	var hashes []string
	for i := 0; i < 5; i++ {
		h, err := s.Write([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		hashes = append(hashes, h)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(list))
	}

	// Verify all hashes are present.
	hashSet := make(map[string]bool)
	for _, h := range list {
		hashSet[h] = true
	}
	for _, h := range hashes {
		if !hashSet[h] {
			t.Fatalf("List missing hash %s", h)
		}
	}
}

func TestListSorted(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 10; i++ {
		if _, err := s.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 1; i < len(list); i++ {
		if list[i] < list[i-1] {
			t.Fatalf("List not sorted: %q comes after %q", list[i], list[i-1])
		}
	}
}

// --- Directory layout ---

func TestDirectoryLayout(t *testing.T) {
	s := tempStore(t)
	data := []byte("layout test")
	hash, _ := s.Write(data)

	// Chunk should be at <root>/<first-2-chars>/<full-hash>
	prefix := hash[:2]
	expectedPath := filepath.Join(s.Root(), prefix, hash)

	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("chunk not at expected path %q: %v", expectedPath, err)
	}
}

// --- Atomic write safety ---

func TestAtomicWriteNoPartialChunks(t *testing.T) {
	s := tempStore(t)

	// Write normally -- verify no temp files are left behind.
	_, err := s.Write([]byte("atomic test"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Check no temp files in root.
	entries, err := os.ReadDir(s.Root())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("unexpected non-directory file in root: %s", e.Name())
		}
	}
}

// --- Concurrent writes ---

func TestConcurrentWrites(t *testing.T) {
	s := tempStore(t)

	var wg sync.WaitGroup
	errs := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte{byte(i)}
			_, err := s.Write(data)
			if err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 100 {
		t.Fatalf("expected 100 chunks after concurrent writes, got %d", len(list))
	}
}

func TestConcurrentWritesSameData(t *testing.T) {
	s := tempStore(t)
	data := []byte("concurrent dedup")

	var wg sync.WaitGroup
	hashes := make([]string, 50)
	errs := make(chan error, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, err := s.Write(data)
			if err != nil {
				errs <- err
				return
			}
			hashes[i] = h
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}

	// All hashes should be identical.
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Fatalf("concurrent writes of same data returned different hashes")
		}
	}

	// Only one chunk should exist.
	list, _ := s.List()
	count := 0
	for _, h := range list {
		if h == hashes[0] {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 chunk after concurrent dedup, got %d", count)
	}
}

// --- Re-write after delete ---

func TestRewriteAfterDelete(t *testing.T) {
	s := tempStore(t)
	data := []byte("rewrite me")

	hash, _ := s.Write(data)
	_ = s.Delete(hash)

	hash2, err := s.Write(data)
	if err != nil {
		t.Fatalf("re-Write: %v", err)
	}
	if hash2 != hash {
		t.Fatal("re-Write returned different hash")
	}

	got, err := s.Read(hash2)
	if err != nil {
		t.Fatalf("Read after re-Write: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("data mismatch after re-Write")
	}
}

// --- Store creation ---

func TestNewCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "nested", "chunks")

	_, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("root dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("root is not a directory")
	}
}

func TestNewExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "existing")
	os.MkdirAll(root, 0o755)

	s, err := New(root)
	if err != nil {
		t.Fatalf("New on existing dir: %v", err)
	}
	if s.Root() != root {
		t.Fatalf("Root() = %q, want %q", s.Root(), root)
	}
}

// --- Data integrity verification ---

func TestReadVerifiesContentIntact(t *testing.T) {
	s := tempStore(t)
	data := []byte("integrity check")
	hash, _ := s.Write(data)

	got, _ := s.Read(hash)
	gotHash := Hash(got)
	if gotHash != hash {
		t.Fatal("data on disk does not match its hash")
	}
}

// --- Compression (D11: CAS chunk compression) ---

func TestWriteCompressesOnDisk(t *testing.T) {
	s := tempStore(t)
	data := []byte("compressible content that should be gzipped on disk")

	hash, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Read raw bytes from disk (bypassing Store.Read decompression).
	raw, err := os.ReadFile(filepath.Join(s.Root(), hash[:2], hash))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Must have gzip magic bytes.
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatal("chunk on disk is not gzip-compressed")
	}

	// Compressed size should differ from original.
	if len(raw) == len(data) {
		t.Fatal("compressed size equals original -- compression not applied?")
	}
}

func TestReadDecompressesTransparently(t *testing.T) {
	s := tempStore(t)
	data := []byte("round-trip through gzip compression")

	hash, _ := s.Write(data)
	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("decompressed data mismatch: got %q, want %q", got, data)
	}
}

func TestReadUncompressedChunk(t *testing.T) {
	s := tempStore(t)
	data := []byte("written without compression")
	hash := Hash(data)

	// Write directly to disk without compression (simulating old format).
	dir := filepath.Join(s.Root(), hash[:2])
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, hash), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Read should return the data as-is (no decompression needed).
	got, err := s.Read(hash)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("uncompressed read mismatch: got %q, want %q", got, data)
	}
}

func TestMixedCompressedAndUncompressed(t *testing.T) {
	s := tempStore(t)

	// Write an uncompressed chunk directly (old format).
	oldData := []byte("old uncompressed chunk from before Phase 5")
	oldHash := Hash(oldData)
	dir := filepath.Join(s.Root(), oldHash[:2])
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, oldHash), oldData, 0o600)

	// Write a new compressed chunk through the normal path.
	newData := []byte("new compressed chunk from Phase 5")
	newHash, _ := s.Write(newData)

	// Both should be readable.
	gotOld, err := s.Read(oldHash)
	if err != nil {
		t.Fatalf("Read old: %v", err)
	}
	if !bytes.Equal(gotOld, oldData) {
		t.Fatal("old uncompressed chunk mismatch")
	}

	gotNew, err := s.Read(newHash)
	if err != nil {
		t.Fatalf("Read new: %v", err)
	}
	if !bytes.Equal(gotNew, newData) {
		t.Fatal("new compressed chunk mismatch")
	}
}

func TestHashStabilityWithCompression(t *testing.T) {
	// Hash must be computed on UNCOMPRESSED data (D11 requirement).
	data := []byte("hash stability test")
	hashDirect := Hash(data)

	s := tempStore(t)
	hashFromWrite, _ := s.Write(data)

	if hashDirect != hashFromWrite {
		t.Fatalf("Write returned different hash than Hash(): %q vs %q", hashFromWrite, hashDirect)
	}
}

func TestCompressionSavesSpace(t *testing.T) {
	s := tempStore(t)

	// Highly compressible data (repeated pattern).
	data := bytes.Repeat([]byte("knowledge graph data "), 1000)
	hash, _ := s.Write(data)

	raw, _ := os.ReadFile(filepath.Join(s.Root(), hash[:2], hash))

	ratio := float64(len(raw)) / float64(len(data))
	if ratio > 0.5 {
		t.Fatalf("compression ratio too high: %.1f%% (expected <50%% for repetitive data)", ratio*100)
	}
}

func TestDeduplicationAcrossFormats(t *testing.T) {
	s := tempStore(t)
	data := []byte("dedup across formats")
	hash := Hash(data)

	// Write uncompressed directly (old format).
	dir := filepath.Join(s.Root(), hash[:2])
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, hash), data, 0o600)

	// Write through Store (would compress) -- should be a no-op
	// because the chunk already exists.
	h2, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if h2 != hash {
		t.Fatal("hash mismatch")
	}

	// The file on disk should still be uncompressed (dedup skipped write).
	raw, _ := os.ReadFile(filepath.Join(dir, hash))
	if bytes.Equal(raw, data) {
		// Still uncompressed -- dedup correctly skipped rewriting.
	} else if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		t.Fatal("dedup should not have rewritten the chunk")
	}
}
