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
	offsets map[string]int
	count   int
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

// Add quantizes a float32 vector to uint8 and appends it to the file.
// If the node already exists, it is replaced (old entry marked as
// tombstone via zero-length ID, reclaimed on next compaction).
func (idx *MmapFlatIndex) Add(nodeID string, vec []float32) {
	if len(vec) != idx.dim {
		return // dimension mismatch, skip silently
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// If node exists, mark old entry as tombstone (overwrite id_len=0).
	if oldOff, exists := idx.offsets[nodeID]; exists {
		idx.file.WriteAt([]byte{0, 0}, int64(oldOff))
		delete(idx.offsets, nodeID)
		idx.count--
	}

	// Quantize float32 -> uint8.
	qvec := quantizeF32ToU8(vec)

	// Build entry: id_len(2) + id + vector.
	entry := make([]byte, 2+len(nodeID)+idx.dim)
	binary.LittleEndian.PutUint16(entry[:2], uint16(len(nodeID)))
	copy(entry[2:], nodeID)
	copy(entry[2+len(nodeID):], qvec)

	// Append to file.
	info, _ := idx.file.Stat()
	offset := info.Size()
	if _, err := idx.file.WriteAt(entry, offset); err != nil {
		return
	}

	idx.count++
	idx.writeHeader(idx.count)
	idx.file.Sync()

	// Remap to include the new data.
	idx.remap()
	idx.offsets[nodeID] = int(offset)
}

// Remove marks an entry as a tombstone (zero id_len).
func (idx *MmapFlatIndex) Remove(nodeID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	off, exists := idx.offsets[nodeID]
	if !exists {
		return
	}
	idx.file.WriteAt([]byte{0, 0}, int64(off))
	delete(idx.offsets, nodeID)
	idx.count--
	idx.writeHeader(idx.count)
	idx.file.Sync()
	idx.remap()
}

// Search performs a brute-force scan using uint8 cosine similarity.
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

	// Quantize query.
	qquery := quantizeF32ToU8(query)

	var results []SearchResult

	if candidates != nil {
		// Filtered scan: only check matching IDs.
		for id := range candidates {
			off, ok := idx.offsets[id]
			if !ok {
				continue
			}
			vec := idx.readVecAt(off)
			if vec == nil {
				continue
			}
			sim := cosineSimU8(qquery, vec)
			results = append(results, SearchResult{NodeID: id, Similarity: sim})
		}
	} else {
		// Full sequential scan.
		for id, off := range idx.offsets {
			vec := idx.readVecAt(off)
			if vec == nil {
				continue
			}
			sim := cosineSimU8(qquery, vec)
			results = append(results, SearchResult{NodeID: id, Similarity: sim})
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

// Close unmaps and closes the underlying file.
func (idx *MmapFlatIndex) Close() error {
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
