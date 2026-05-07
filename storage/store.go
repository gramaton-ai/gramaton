package storage

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Store is a content-addressed chunk store backed by the filesystem.
// Chunks are identified by their SHA-256 content hash. The on-disk layout
// splits the hash into a 2-character prefix directory for filesystem
// friendliness:
//
//	<root>/
//	  ab/
//	    abcdef1234...  (full hash as filename)
//	  cd/
//	    cdef5678...
//
// All writes are atomic: data is written to a temp file in the root
// directory, then renamed to the final path. This ensures no partial
// chunks exist on disk even if the process crashes mid-write.
type Store struct {
	root string
}

// New creates a Store rooted at the given directory. The directory is
// created if it does not exist.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("storage: create root %q: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Root returns the store's root directory path.
func (s *Store) Root() string {
	return s.root
}

// Write stores data and returns its content hash. If a chunk with the
// same hash already exists, the write is a no-op (content-addressed
// deduplication). The write is atomic: temp file + rename.
//
// Data is hashed before compression (hash stability), then gzip
// compressed on disk. Read auto-detects the format via magic bytes.
// Old uncompressed chunks coexist with new compressed chunks.
func (s *Store) Write(data []byte) (string, error) {
	hash := Hash(data)

	path, err := s.chunkPath(hash)
	if err != nil {
		return "", err
	}

	// Fast path: chunk already exists (possibly in old uncompressed format).
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}

	// Compress the data for storage.
	compressed, err := gzipCompress(data)
	if err != nil {
		return "", fmt.Errorf("storage: compress chunk: %w", err)
	}

	// Ensure prefix directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("storage: create dir %q: %w", dir, err)
	}

	// Atomic write: temp file in root, then rename.
	tmp, err := os.CreateTemp(s.root, ".chunk-*")
	if err != nil {
		return "", fmt.Errorf("storage: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up temp file on any error.
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(compressed); err != nil {
		return "", fmt.Errorf("storage: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("storage: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("storage: close temp file: %w", err)
	}

	if err := renameWithDedupRecovery(tmpPath, path); err != nil {
		return "", fmt.Errorf("storage: rename %q to %q: %w", tmpPath, path, err)
	}
	// Fsync the parent directory so the rename(2) is durable. Without
	// this, a crash after rename but before the next dir-sync can
	// lose the entry on ext4 with certain mount options and on older
	// filesystems. The advertised "atomic, content-addressed" store
	// must actually be atomic on disk.
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return "", fmt.Errorf("storage: fsync parent dir: %w", err)
	}

	success = true
	return hash, nil
}

// renameWithDedupRecovery atomically renames src to dest. If the
// underlying rename fails AND dest now exists, treats the operation
// as a successful no-op: content-addressed storage means whatever is
// at dest holds the same content the caller was about to write.
//
// Linux/macOS: os.Rename silently overwrites an existing dest, so
// this helper acts identically to a bare os.Rename in the common
// case. Windows: os.Rename returns EACCES when dest exists or is
// held by another process; the dedup-recovery branch turns that into
// success when dest is present.
func renameWithDedupRecovery(src, dest string) error {
	if err := os.Rename(src, dest); err != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// fsyncDir opens the given directory and fsyncs it. Required after
// rename(2) for full durability of the directory entry change.
//
// No-op on Windows: see the fsyncDir doc comment in core/refs.go.
func fsyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Read returns the data for the given content hash. Returns an error
// wrapping ErrNotFound if the chunk does not exist. Transparently
// decompresses gzip-compressed chunks (detected via magic bytes).
func (s *Store) Read(hash string) ([]byte, error) {
	path, err := s.chunkPath(hash)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("storage: chunk %s: %w", hash, ErrNotFound)
		}
		return nil, fmt.Errorf("storage: read chunk %s: %w", hash, err)
	}
	if isGzipped(raw) {
		data, err := gzipDecompress(raw)
		if err != nil {
			return nil, fmt.Errorf("storage: decompress chunk %s: %w", hash, err)
		}
		return data, nil
	}
	return raw, nil
}

// Has reports whether a chunk with the given hash exists.
func (s *Store) Has(hash string) bool {
	path, err := s.chunkPath(hash)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// Delete removes a chunk by hash. Returns ErrNotFound if the chunk
// does not exist.
func (s *Store) Delete(hash string) error {
	path, err := s.chunkPath(hash)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storage: chunk %s: %w", hash, ErrNotFound)
		}
		return fmt.Errorf("storage: delete chunk %s: %w", hash, err)
	}
	return nil
}

// List returns all chunk hashes in the store, sorted lexicographically.
func (s *Store) List() ([]string, error) {
	var hashes []string

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("storage: read root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 2 {
			continue
		}
		subdir := filepath.Join(s.root, entry.Name())
		chunks, err := os.ReadDir(subdir)
		if err != nil {
			return nil, fmt.Errorf("storage: read subdir %q: %w", subdir, err)
		}
		for _, chunk := range chunks {
			if !chunk.IsDir() {
				hashes = append(hashes, chunk.Name())
			}
		}
	}

	sort.Strings(hashes)
	return hashes, nil
}

// chunkPath returns the filesystem path for a given content hash.
// Layout: <root>/<first-2-chars>/<full-hash>
// Returns an error if the hash is not a valid hex string.
func (s *Store) chunkPath(hash string) (string, error) {
	if !isValidHex(hash) || len(hash) < 2 {
		return "", fmt.Errorf("storage: invalid hash %q", hash)
	}
	return filepath.Join(s.root, hash[:2], hash), nil
}

// isValidHex reports whether s contains only lowercase hex characters.
func isValidHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

// ErrNotFound is returned when a chunk does not exist in the store.
var ErrNotFound = errors.New("not found")

// --- Compression helpers (D11: CAS chunk compression) ---

// isGzipped checks for the gzip magic bytes (0x1f 0x8b) at the start
// of data. Used to auto-detect compressed vs uncompressed chunks on read.
func isGzipped(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// gzipCompress compresses data using gzip with default compression level.
func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gzipDecompress decompresses gzip data. Limits decompressed size to
// 100MB to prevent compression bomb attacks.
func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	const maxDecompressedSize = 100 * 1024 * 1024 // 100MB
	lr := io.LimitReader(r, maxDecompressedSize+1)
	out, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressedSize {
		return nil, fmt.Errorf("decompressed data exceeds %d bytes limit", maxDecompressedSize)
	}
	return out, nil
}
