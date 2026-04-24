package index

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"

	"github.com/gramaton-ai/gramaton/internal/mmap"
)

// MmapFlatIndex is a disk-backed flat vector index using mmap (D1 revised).
//
// File layout:
//
//	header (32 bytes):
//	  magic    [4]byte   "VFLT"
//	  version  uint16    1
//	  dim      uint16    vector dimension (e.g., 384)
//	  count    uint32    number of entries
//	  reserved [20]byte
//
//	entries (repeated count times):
//	  id_len   uint16
//	  id       [id_len]byte   node ID (ULID, typically 26 bytes)
//	  vector   [dim]byte      uint8 quantized vector
//
// Vectors are stored as uint8 (quantized from float32 on Add).
// Search computes uint8 cosine similarity via dot product divided by norms (full cosine).
// The file supports O(1) append (extend + write entry + update count).
type MmapFlatIndex struct {
	mu     sync.RWMutex
	path   string
	file   *os.File
	region *mmap.Region // platform-abstracted mmap handle
	data   []byte       // alias of region.Bytes() cached for hot-path access
	dim    int
	qscale float32 // quantization scale for this dimension

	// In-memory ID-to-offset map for filtered scans and Remove.
	// offset points to the start of the entry (id_len field).
	// Negative offsets indicate the vector is in the write buffer
	// (buffer index = -(offset+1)).
	offsets map[string]int
	count   int

	// Write buffer: new vectors accumulate here until Flush.
	// Avoids remap+fsync on every Add.
	buffer []bufferedEntry

	// hasTombstones is set when Remove or Add(replace) writes a
	// tombstone (idLen=0) into the mmap'd region. Tombstones break
	// buildOffsetMap on reopen because the original idLen is lost,
	// so Flush MUST rewrite the file from scratch when this is true,
	// even if buffer is empty.
	hasTombstones bool
}

type bufferedEntry struct {
	nodeID string
	vec    []byte // uint8 quantized
}

const (
	flatMagic      = "VFLT"
	flatVersion    = 1
	flatHeaderSize = 32
)

// NewMmapFlatIndex opens or creates a flat mmap'd vector index.
// dim is the vector dimension (e.g., 384).
func NewMmapFlatIndex(path string, dim int) (*MmapFlatIndex, error) {
	idx := &MmapFlatIndex{
		path:    path,
		dim:     dim,
		qscale:  quantizationScale(dim),
		offsets: make(map[string]int),
	}

	// Open or create the file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("flat index: open %s: %w", path, err)
	}
	idx.file = f

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("flat index: stat: %w", err)
	}

	if info.Size() == 0 {
		// New file: write header.
		if err := idx.writeHeader(0); err != nil {
			f.Close()
			return nil, err
		}
	}

	// Mmap the file.
	if err := idx.remap(); err != nil {
		f.Close()
		return nil, err
	}

	// Validate header.
	if string(idx.data[:4]) != flatMagic {
		f.Close()
		return nil, fmt.Errorf("flat index: invalid magic in %s", path)
	}
	fileDim := int(binary.LittleEndian.Uint16(idx.data[6:8]))
	if fileDim != dim {
		f.Close()
		return nil, fmt.Errorf("flat index: dimension mismatch: file has %d, expected %d", fileDim, dim)
	}
	idx.count = int(binary.LittleEndian.Uint32(idx.data[8:12]))

	// Build the offset map by scanning entries.
	if err := idx.buildOffsetMap(); err != nil {
		f.Close()
		return nil, err
	}

	return idx, nil
}

func (idx *MmapFlatIndex) writeHeader(count int) error {
	hdr := make([]byte, flatHeaderSize)
	copy(hdr[:4], flatMagic)
	binary.LittleEndian.PutUint16(hdr[4:6], flatVersion)
	binary.LittleEndian.PutUint16(hdr[6:8], uint16(idx.dim))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(count))
	_, err := idx.file.WriteAt(hdr, 0)
	return err
}

func (idx *MmapFlatIndex) remap() error {
	// Release any existing mapping before establishing the new one.
	// Close errors here are non-fatal: the new mapping supersedes the
	// old regardless, and propagating them would mask the real error
	// we care about (the new mmap.Open failure, if any).
	if idx.region != nil {
		_ = idx.region.Close()
		idx.region = nil
		idx.data = nil
	}

	info, err := idx.file.Stat()
	if err != nil {
		return fmt.Errorf("flat index: stat for mmap: %w", err)
	}
	size := info.Size()
	if size < flatHeaderSize {
		return fmt.Errorf("flat index: file too small (%d bytes)", size)
	}

	region, err := mmap.Open(idx.file, int(size))
	if err != nil {
		return fmt.Errorf("flat index: mmap: %w", err)
	}
	idx.region = region
	idx.data = region.Bytes()
	return nil
}

func (idx *MmapFlatIndex) buildOffsetMap() error {
	idx.offsets = make(map[string]int, idx.count)
	pos := flatHeaderSize
	for i := 0; i < idx.count; i++ {
		if pos+2 > len(idx.data) {
			return fmt.Errorf("flat index: truncated at entry %d", i)
		}
		idLen := int(binary.LittleEndian.Uint16(idx.data[pos:]))
		entryStart := pos
		pos += 2 + idLen + idx.dim
		if pos > len(idx.data) {
			return fmt.Errorf("flat index: truncated at entry %d", i)
		}
		nodeID := string(idx.data[entryStart+2 : entryStart+2+idLen])
		idx.offsets[nodeID] = entryStart
	}
	return nil
}

// Add quantizes a float32 vector to uint8 and buffers it. The vector
// becomes immediately searchable (buffer is checked during Search).
// Call Flush to write buffered vectors to disk.
func (idx *MmapFlatIndex) Add(nodeID string, vec []float32) {
	if len(vec) != idx.dim {
		return
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// If node exists in mmap'd data, mark as tombstone.
	if oldOff, exists := idx.offsets[nodeID]; exists && oldOff >= 0 {
		idx.file.WriteAt([]byte{0, 0}, int64(oldOff))
		idx.hasTombstones = true
	}

	// If node exists in buffer, remove old entry.
	if oldOff, exists := idx.offsets[nodeID]; exists && oldOff < 0 {
		bufIdx := -(oldOff + 1)
		if bufIdx < len(idx.buffer) {
			idx.buffer[bufIdx].nodeID = "" // mark as dead
		}
	}

	if _, exists := idx.offsets[nodeID]; exists {
		idx.count--
	}

	// Buffer the new vector.
	qvec := quantizeF32ToU8(vec, idx.qscale)
	idx.buffer = append(idx.buffer, bufferedEntry{nodeID: nodeID, vec: qvec})
	idx.offsets[nodeID] = -(len(idx.buffer)) // negative: -(bufferIndex+1)
	idx.count++
}

// Remove marks an entry as a tombstone or removes it from the buffer.
//
// Tombstones are persistence-only: the file.WriteAt below blanks
// the entry's idLen on disk, but the read path (Search) consults
// the in-memory offsets map -- which Remove deletes the same call.
// Code MUST NOT trust the mmap view for entries that have been
// tombstoned in the same session; the page cache makes the new
// bytes visible eventually but the ordering is not guaranteed
// against the WriteAt / mmap view consistency model on macOS.
// Compaction-on-Flush (see Flush + rewriteFromOffsetsLocked)
// reconciles disk and offsets atomically.
func (idx *MmapFlatIndex) Remove(nodeID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	off, exists := idx.offsets[nodeID]
	if !exists {
		return
	}

	if off >= 0 {
		// In mmap'd data: tombstone it.
		idx.file.WriteAt([]byte{0, 0}, int64(off))
		idx.hasTombstones = true
	} else {
		// In buffer: mark dead.
		bufIdx := -(off + 1)
		if bufIdx < len(idx.buffer) {
			idx.buffer[bufIdx].nodeID = ""
		}
	}

	delete(idx.offsets, nodeID)
	idx.count--
}

// Flush writes all buffered vectors to disk, updates the header,
// syncs, and remaps. Call this from Engine.Save and Engine.Close.
//
// When tombstones exist (Remove or Add-replace was called), Flush
// rewrites the entire file from scratch. This is required because
// tombstones overwrite the idLen field with zero, which destroys the
// information needed for buildOffsetMap to walk past them on reopen.
// Append-only flush would persist a corrupted file. Compaction
// guarantees the on-disk layout matches the in-memory offsets.
func (idx *MmapFlatIndex) Flush() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if len(idx.buffer) == 0 && !idx.hasTombstones {
		return nil
	}

	// Collect live entries from the buffer (skip dead entries from
	// Remove/Replace).
	var liveBuffered []bufferedEntry
	for _, e := range idx.buffer {
		if e.nodeID != "" {
			liveBuffered = append(liveBuffered, e)
		}
	}

	// Tombstones present: rewrite the entire file. The on-disk
	// format cannot represent a tombstone in a way buildOffsetMap
	// can skip (the original idLen is gone), so a fresh write is
	// the only correct option.
	if idx.hasTombstones {
		return idx.rewriteFromOffsetsLocked(liveBuffered)
	}

	if len(liveBuffered) == 0 {
		idx.buffer = nil
		return nil
	}

	// Fast path: no tombstones, only new entries -> append.
	entrySize := 2 + 26 + idx.dim // typical ULID is 26 bytes
	buf := make([]byte, 0, len(liveBuffered)*entrySize)
	info, _ := idx.file.Stat()
	baseOffset := info.Size()

	for _, e := range liveBuffered {
		entry := make([]byte, 2+len(e.nodeID)+idx.dim)
		binary.LittleEndian.PutUint16(entry[:2], uint16(len(e.nodeID)))
		copy(entry[2:], e.nodeID)
		copy(entry[2+len(e.nodeID):], e.vec)
		buf = append(buf, entry...)
	}

	// Single write for all entries.
	if _, err := idx.file.WriteAt(buf, baseOffset); err != nil {
		return fmt.Errorf("flat index: flush write: %w", err)
	}

	// Update offsets to point to mmap'd positions.
	pos := int(baseOffset)
	for _, e := range liveBuffered {
		idx.offsets[e.nodeID] = pos
		pos += 2 + len(e.nodeID) + idx.dim
	}

	idx.buffer = nil
	if err := idx.writeHeader(idx.count); err != nil {
		return fmt.Errorf("flat index: write header: %w", err)
	}
	if err := idx.file.Sync(); err != nil {
		return fmt.Errorf("flat index: sync: %w", err)
	}
	return idx.remap()
}

// rewriteFromOffsetsLocked compacts the index by reading every live
// vector (from mmap or buffer), then rewriting the file from scratch
// with header + entries (no tombstones). Caller must hold idx.mu.
//
// Required when hasTombstones is true because buildOffsetMap cannot
// skip tombstones safely (the on-disk format loses the original idLen).
func (idx *MmapFlatIndex) rewriteFromOffsetsLocked(liveBuffered []bufferedEntry) error {
	// Snapshot every live entry as (id, qvec) pairs. Reading from
	// mmap before truncate is safe; we materialise into Go-owned
	// memory before remapping.
	type liveEntry struct {
		id  string
		vec []byte
	}
	live := make([]liveEntry, 0, len(idx.offsets))

	// Track buffered IDs so we don't double-add when iterating offsets
	// (offsets entries with negative offset are buffered).
	for id, off := range idx.offsets {
		if off < 0 {
			continue // handled in the buffered loop below
		}
		vec := idx.readVecAt(off)
		if vec == nil {
			continue // tombstoned (shouldn't be in offsets, but be defensive)
		}
		// Copy out of mmap before we munmap.
		copied := make([]byte, len(vec))
		copy(copied, vec)
		live = append(live, liveEntry{id: id, vec: copied})
	}
	for _, e := range liveBuffered {
		live = append(live, liveEntry{id: e.nodeID, vec: e.vec})
	}

	// Truncate file back to header-only, then write fresh entries.
	// Unmap first so we can resize.
	if idx.region != nil {
		_ = idx.region.Close()
		idx.region = nil
		idx.data = nil
	}
	if err := idx.file.Truncate(flatHeaderSize); err != nil {
		return fmt.Errorf("flat index: truncate: %w", err)
	}

	// Build serialised entries.
	totalSize := 0
	for _, e := range live {
		totalSize += 2 + len(e.id) + idx.dim
	}
	buf := make([]byte, 0, totalSize)
	for _, e := range live {
		entry := make([]byte, 2+len(e.id)+idx.dim)
		binary.LittleEndian.PutUint16(entry[:2], uint16(len(e.id)))
		copy(entry[2:], e.id)
		copy(entry[2+len(e.id):], e.vec)
		buf = append(buf, entry...)
	}

	if len(buf) > 0 {
		if _, err := idx.file.WriteAt(buf, flatHeaderSize); err != nil {
			return fmt.Errorf("flat index: rewrite write: %w", err)
		}
	}

	// Rebuild the offsets map for the new layout.
	idx.offsets = make(map[string]int, len(live))
	pos := flatHeaderSize
	for _, e := range live {
		idx.offsets[e.id] = pos
		pos += 2 + len(e.id) + idx.dim
	}
	idx.count = len(live)
	idx.buffer = nil
	idx.hasTombstones = false

	if err := idx.writeHeader(idx.count); err != nil {
		return fmt.Errorf("flat index: write header: %w", err)
	}
	if err := idx.file.Sync(); err != nil {
		return fmt.Errorf("flat index: sync: %w", err)
	}
	return idx.remap()
}

// Search performs a brute-force scan using uint8 cosine similarity.
// Checks both mmap'd data and the write buffer.
// If candidates is non-nil, only those IDs are checked (filtered scan).
func (idx *MmapFlatIndex) Search(query []float32, k int, candidates map[string]struct{}) []SearchResult {
	if k <= 0 {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.offsets) == 0 {
		return nil
	}

	qquery := quantizeF32ToU8(query, idx.qscale)
	var results []SearchResult

	search := func(id string) {
		off, ok := idx.offsets[id]
		if !ok {
			return
		}
		var vec []byte
		if off >= 0 {
			vec = idx.readVecAt(off)
		} else {
			bufIdx := -(off + 1)
			if bufIdx < len(idx.buffer) && idx.buffer[bufIdx].nodeID != "" {
				vec = idx.buffer[bufIdx].vec
			}
		}
		if vec == nil {
			return
		}
		sim := cosineSimU8(qquery, vec)
		results = append(results, SearchResult{NodeID: id, Similarity: sim})
	}

	if candidates != nil {
		for id := range candidates {
			search(id)
		}
	} else {
		for id := range idx.offsets {
			search(id)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})
	if k < len(results) {
		results = results[:k]
	}
	return results
}

func (idx *MmapFlatIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.offsets)
}

// Close flushes buffered vectors, unmaps, and closes the file.
func (idx *MmapFlatIndex) Close() error {
	// Flush outside the lock (Flush takes its own lock).
	idx.Flush()

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.region != nil {
		_ = idx.region.Close()
		idx.region = nil
		idx.data = nil
	}
	if idx.file != nil {
		return idx.file.Close()
	}
	return nil
}

// readVecAt reads the uint8 vector from the mmap'd data at the given
// entry offset. Returns nil if the entry is a tombstone.
func (idx *MmapFlatIndex) readVecAt(entryOffset int) []byte {
	if entryOffset+2 > len(idx.data) {
		return nil
	}
	idLen := int(binary.LittleEndian.Uint16(idx.data[entryOffset:]))
	if idLen == 0 {
		return nil // tombstone
	}
	vecStart := entryOffset + 2 + idLen
	vecEnd := vecStart + idx.dim
	if vecEnd > len(idx.data) {
		return nil
	}
	return idx.data[vecStart:vecEnd]
}

// --- Quantization ---

// quantizationScale returns the symmetric quantization range [-s, s]
// appropriate for L2-normalized vectors of the given dimension.
// For high-dimensional normalized embeddings, individual components
// are small (~1/sqrt(dim)); the scale ensures full uint8 resolution
// across the meaningful signal range. Low-dimensional vectors (tests)
// get the full [-1, 1] range.
func quantizationScale(dim int) float32 {
	if dim <= 16 {
		return 1.0
	}
	// 4/sqrt(dim) covers ~4 standard deviations of L2-normalized components.
	s := float32(4.0 / math.Sqrt(float64(dim)))
	if s > 1.0 {
		s = 1.0
	}
	return s
}

// quantizeF32ToU8 maps float32 values from [-scale, scale] to [0, 255].
// Values outside the range are clamped. The scale is dimension-aware:
// tight for high-dimensional normalized embeddings (full uint8 resolution),
// wide for low-dimensional vectors (testing, general use).
//
// Using a fixed range (not per-vector min-max) is critical for cosine
// similarity: per-vector scaling destroys magnitude information, causing
// vectors with similar shapes but different scales to appear identical.
func quantizeF32ToU8(vec []float32, scale float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	invScale := 1.0 / scale
	out := make([]byte, len(vec))
	for i, v := range vec {
		// Clamp to [-scale, scale], map to [0, 255].
		normalized := v * invScale // [-1, 1]
		if normalized < -1 {
			normalized = -1
		} else if normalized > 1 {
			normalized = 1
		}
		out[i] = byte((normalized + 1) * 0.5 * 255)
	}
	return out
}

// cosineSimU8 computes cosine similarity between two uint8 vectors
// stored in the shifted quantization scheme (128 represents zero,
// 0 and 255 represent the negative and positive ends of the
// quantization range respectively).
//
// The shift MUST be removed before computing the cosine. Computing
// dot/norms on the raw bytes yields a similarity dominated by the
// constant 128 offset, not the underlying signal: two near-orthogonal
// L2-normalised vectors would score ~0.99 because their shifted
// representations are close to (128, 128, ..., 128) which has very
// high self-cosine.
func cosineSimU8(a, b []byte) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB int64
	for i := range a {
		// Re-centre to signed offsets from zero. int8 isn't quite
		// right (range -128..127 vs -127..128), but the asymmetry
		// is at most 1 LSB and harmless for cosine.
		ai := int64(a[i]) - 128
		bi := int64(b[i]) - 128
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(float64(normA)) * math.Sqrt(float64(normB))
	if denom == 0 {
		return 0
	}
	return float32(float64(dot) / denom)
}

// Verify interface compliance.
var _ VectorIndex = (*MmapFlatIndex)(nil)
