package index

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
)

// BloomFilter is a space-efficient probabilistic set. It can tell you
// "definitely not in set" or "possibly in set". Used for pre-filtering
// BM25 candidates: skip nodes that can't possibly contain a query term.
//
// Each node in a BM25 layer has a bloom filter over its terms. At query
// time, check if all query terms pass the bloom filter before running
// the full BM25 scoring.
type BloomFilter struct {
	bits []uint64
	k    int // number of hash functions
	m    int // number of bits
	n    int // number of items added
}

// NewBloomFilter creates a bloom filter sized for the expected number
// of items at the given false positive rate.
func NewBloomFilter(expectedItems int, fpRate float64) *BloomFilter {
	if expectedItems <= 0 {
		expectedItems = 100
	}
	if fpRate <= 0 || fpRate >= 1 {
		fpRate = 0.01
	}

	// m = -n*ln(p) / (ln2)^2
	m := int(math.Ceil(-float64(expectedItems) * math.Log(fpRate) / (math.Ln2 * math.Ln2)))
	if m < 64 {
		m = 64
	}

	// k = (m/n) * ln2
	k := int(math.Ceil(float64(m) / float64(expectedItems) * math.Ln2))
	if k < 1 {
		k = 1
	}
	if k > 20 {
		k = 20
	}

	words := (m + 63) / 64
	return &BloomFilter{
		bits: make([]uint64, words),
		k:    k,
		m:    m,
	}
}

// Add inserts an item into the bloom filter.
func (bf *BloomFilter) Add(item string) {
	for i := 0; i < bf.k; i++ {
		pos := bf.hash(item, i) % uint64(bf.m)
		bf.bits[pos/64] |= 1 << (pos % 64)
	}
	bf.n++
}

// Contains returns true if the item might be in the set (possible
// false positive) or false if the item is definitely not in the set.
func (bf *BloomFilter) Contains(item string) bool {
	for i := 0; i < bf.k; i++ {
		pos := bf.hash(item, i) % uint64(bf.m)
		if bf.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false
		}
	}
	return true
}

// hash produces the i-th hash value for an item using double hashing:
// h(i) = h1 + i*h2, where h1 and h2 come from FNV-1a.
func (bf *BloomFilter) hash(item string, i int) uint64 {
	h := fnv.New64a()
	h.Write([]byte(item))
	h1 := h.Sum64()
	h.Write([]byte{0xFF}) // separator
	h2 := h.Sum64()
	return h1 + uint64(i)*h2
}

// Count returns the number of items added.
func (bf *BloomFilter) Count() int { return bf.n }

// MarshalBinary serializes the bloom filter.
func (bf *BloomFilter) MarshalBinary() ([]byte, error) {
	// header: magic(4) + version(2) + k(2) + m(4) + n(4) + bits
	buf := make([]byte, 0, 16+len(bf.bits)*8)
	buf = append(buf, 'B', 'L', 'O', 'M')
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(bf.k))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(bf.m))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(bf.n))
	for _, word := range bf.bits {
		buf = binary.LittleEndian.AppendUint64(buf, word)
	}
	return buf, nil
}

const (
	maxBloomBits = 1_000_000_000 // ~125MB maximum bloom filter size
	maxBloomK    = 100           // reasonable hash function limit
)

// UnmarshalBinary restores a bloom filter from binary data.
func (bf *BloomFilter) UnmarshalBinary(data []byte) error {
	if len(data) < 16 || string(data[:4]) != "BLOM" {
		return errBloomInvalid
	}
	// version := binary.LittleEndian.Uint16(data[4:6])
	bf.k = int(binary.LittleEndian.Uint16(data[6:8]))
	bf.m = int(binary.LittleEndian.Uint32(data[8:12]))
	bf.n = int(binary.LittleEndian.Uint32(data[12:16]))

	if bf.m > maxBloomBits {
		return fmt.Errorf("bloom: m %d exceeds maximum %d", bf.m, maxBloomBits)
	}
	if bf.k > maxBloomK {
		return fmt.Errorf("bloom: k %d exceeds maximum %d", bf.k, maxBloomK)
	}

	words := (bf.m + 63) / 64
	expected := 16 + words*8
	if len(data) < expected {
		return fmt.Errorf("bloom: data truncated: have %d bytes, need %d", len(data), expected)
	}
	bf.bits = make([]uint64, words)
	pos := 16
	for i := range bf.bits {
		bf.bits[i] = binary.LittleEndian.Uint64(data[pos:])
		pos += 8
	}
	return nil
}

var errBloomInvalid = &bloomError{"invalid bloom filter data"}

type bloomError struct{ msg string }

func (e *bloomError) Error() string { return "bloom: " + e.msg }

// BloomIndex maps node IDs to per-node bloom filters over their terms.
// Used for pre-filtering: "can node X possibly match query term Y?"
type BloomIndex struct {
	filters map[string]*BloomFilter
}

// NewBloomIndex creates an empty bloom index.
func NewBloomIndex() *BloomIndex {
	return &BloomIndex{filters: make(map[string]*BloomFilter)}
}

// AddTerms creates or updates the bloom filter for a node with the given terms.
func (bi *BloomIndex) AddTerms(nodeID string, terms []string) {
	bf := NewBloomFilter(len(terms), 0.01)
	for _, t := range terms {
		bf.Add(t)
	}
	bi.filters[nodeID] = bf
}

// Remove deletes the bloom filter for a node.
func (bi *BloomIndex) Remove(nodeID string) {
	delete(bi.filters, nodeID)
}

// MayContainAll returns true if the node's bloom filter passes for ALL
// query terms. Returns true if no filter exists (conservative).
func (bi *BloomIndex) MayContainAll(nodeID string, terms []string) bool {
	bf, ok := bi.filters[nodeID]
	if !ok {
		return true // no filter = can't reject
	}
	for _, t := range terms {
		if !bf.Contains(t) {
			return false
		}
	}
	return true
}

// Len returns the number of nodes with bloom filters.
func (bi *BloomIndex) Len() int { return len(bi.filters) }
