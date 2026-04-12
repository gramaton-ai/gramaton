package index

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"syscall"
)

// MmapFlatIndex is a disk-backed flat vector index using mmap (D1 revised).
//
// File layout:
//
//	header (32 bytes):
//	  magic    [4]byte   "VFLAT"[0:4]
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
// Search computes uint8 cosine similarity via dot product.
// The file supports O(1) append (extend + write entry + update count).
type MmapFlatIndex struct {
	mu   sync.RWMutex
	path string
	file *os.File
	data []byte // mmap'd region (nil if empty file)
	dim  int

	// In-memory ID-to-offset map for filtered scans and Remove.
	// offset points to the start of the entry (id_len field).
	// Negative offsets indicate the vector is in the write buffer
	// (buffer index = -(offset+1)).
	offsets map[string]int
	count   int

	// Write buffer: new vectors accumulate here until Flush.
	// Avoids remap+fsync on every Add.
	buffer []bufferedEntry
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
	// Unmap existing.
	if idx.data != nil {
		syscall.Munmap(idx.data)
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

	data, err := syscall.Mmap(int(idx.file.Fd()), 0, int(size),
		syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("flat index: mmap: %w", err)
	}
	idx.data = data
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
	qvec := quantizeF32ToU8(vec)
	idx.buffer = append(idx.buffer, bufferedEntry{nodeID: nodeID, vec: qvec})
	idx.offsets[nodeID] = -(len(idx.buffer)) // negative: -(bufferIndex+1)
	idx.count++
}

// Remove marks an entry as a tombstone or removes it from the buffer.
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
func (idx *MmapFlatIndex) Flush() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if len(idx.buffer) == 0 {
		return nil
	}

	// Collect live entries (skip dead entries from Remove/Replace).
	var live []bufferedEntry
	for _, e := range idx.buffer {
		if e.nodeID != "" {
			live = append(live, e)
		}
	}

	if len(live) == 0 {
		idx.buffer = nil
		// Still need to update header for tombstoned entries.
		idx.writeHeader(idx.count)
		idx.file.Sync()
		idx.remap()
		return nil
	}

	// Build a single write buffer for all entries.
	entrySize := 2 + 26 + idx.dim // typical ULID is 26 bytes
	buf := make([]byte, 0, len(live)*entrySize)
	info, _ := idx.file.Stat()
	baseOffset := info.Size()

	for _, e := range live {
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
	for _, e := range live {
		idx.offsets[e.nodeID] = pos
		pos += 2 + len(e.nodeID) + idx.dim
	}

	idx.buffer = nil
	idx.writeHeader(idx.count)
	idx.file.Sync()
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

	qquery := quantizeF32ToU8(query)
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
	if idx.data != nil {
		syscall.Munmap(idx.data)
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

// quantizeF32ToU8 maps float32 values to [0, 255] using min-max scaling.
// This preserves relative distances for cosine similarity.
func quantizeF32ToU8(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	min, max := vec[0], vec[0]
	for _, v := range vec[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	if span == 0 {
		// All values identical -- map to 128.
		out := make([]byte, len(vec))
		for i := range out {
			out[i] = 128
		}
		return out
	}
	out := make([]byte, len(vec))
	for i, v := range vec {
		normalized := (v - min) / span // [0, 1]
		out[i] = byte(normalized * 255)
	}
	return out
}

// cosineSimU8 computes cosine similarity between two uint8 vectors.
func cosineSimU8(a, b []byte) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB uint64
	for i := range a {
		ai, bi := uint64(a[i]), uint64(b[i])
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
