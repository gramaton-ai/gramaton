package index

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// BM25Index provides term-frequency / inverse-document-frequency based
// scoring using the Okapi BM25 algorithm. It runs alongside the vector
// index to enable hybrid search (vector + keyword).
//
// Not thread-safe -- the server layer handles serialization.
type BM25Index struct {
	// Term frequency: nodeID -> token -> count.
	tf map[string]map[string]int

	// Document lengths: nodeID -> total token count.
	docLen map[string]int

	// Inverted index: token -> set of nodeIDs containing it.
	inverted map[string]map[string]struct{}

	// Total number of documents.
	numDocs int

	// Average document length (maintained incrementally).
	avgDL float64

	// BM25 parameters.
	k1 float64 // term frequency saturation (default 1.2)
	b  float64 // length normalization (default 0.75)
}

// NewBM25Index creates a new empty BM25 index with the given parameters.
// Use k1=0 and b=0 for defaults (1.2 and 0.75 respectively).
func NewBM25Index(k1, b float64) *BM25Index {
	if k1 == 0 {
		k1 = 1.2
	}
	if b == 0 {
		b = 0.75
	}
	return &BM25Index{
		tf:       make(map[string]map[string]int),
		docLen:   make(map[string]int),
		inverted: make(map[string]map[string]struct{}),
		k1:       k1,
		b:        b,
	}
}

// Add indexes a document's content. Tokenizes the text and stores term
// frequencies. If the nodeID already exists, it is replaced.
func (idx *BM25Index) Add(nodeID, text string) {
	// Remove old entry if exists.
	if _, exists := idx.tf[nodeID]; exists {
		idx.removeInternal(nodeID)
	}

	tokens := Tokenize(text)
	if len(tokens) == 0 {
		return
	}

	tf := make(map[string]int, len(tokens)/2)
	for _, t := range tokens {
		tf[t]++
	}

	idx.tf[nodeID] = tf
	idx.docLen[nodeID] = len(tokens)

	for token := range tf {
		if idx.inverted[token] == nil {
			idx.inverted[token] = make(map[string]struct{})
		}
		idx.inverted[token][nodeID] = struct{}{}
	}

	idx.numDocs++
	idx.recomputeAvgDL()
}

// Remove deletes a document from the index.
func (idx *BM25Index) Remove(nodeID string) {
	if _, exists := idx.tf[nodeID]; !exists {
		return
	}
	idx.removeInternal(nodeID)
	idx.recomputeAvgDL()
}

func (idx *BM25Index) removeInternal(nodeID string) {
	for token := range idx.tf[nodeID] {
		if set, ok := idx.inverted[token]; ok {
			delete(set, nodeID)
			if len(set) == 0 {
				delete(idx.inverted, token)
			}
		}
	}
	delete(idx.tf, nodeID)
	delete(idx.docLen, nodeID)
	idx.numDocs--
}

func (idx *BM25Index) recomputeAvgDL() {
	if idx.numDocs == 0 {
		idx.avgDL = 0
		return
	}
	total := 0
	for _, dl := range idx.docLen {
		total += dl
	}
	idx.avgDL = float64(total) / float64(idx.numDocs)
}

// Search scores all documents in the optional candidate set against
// the query tokens using BM25. Returns the top-k results sorted by
// descending score. If candidates is nil, all documents are scored.
func (idx *BM25Index) Search(queryTokens []string, k int, candidates map[string]struct{}) []SearchResult {
	if len(queryTokens) == 0 || idx.numDocs == 0 {
		return nil
	}

	// Deduplicate query tokens.
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, t := range queryTokens {
		querySet[t] = struct{}{}
	}

	scores := make(map[string]float64)

	for token := range querySet {
		docSet, ok := idx.inverted[token]
		if !ok {
			continue
		}

		// IDF: log((N - n + 0.5) / (n + 0.5) + 1)
		n := float64(len(docSet))
		idf := math.Log((float64(idx.numDocs)-n+0.5)/(n+0.5) + 1)

		for nodeID := range docSet {
			if candidates != nil {
				if _, ok := candidates[nodeID]; !ok {
					continue
				}
			}

			tf := float64(idx.tf[nodeID][token])
			dl := float64(idx.docLen[nodeID])

			// BM25: idf * (tf * (k1 + 1)) / (tf + k1 * (1 - b + b * dl/avgdl))
			denom := tf + idx.k1*(1-idx.b+idx.b*dl/idx.avgDL)
			score := idf * (tf * (idx.k1 + 1)) / denom

			scores[nodeID] += score
		}
	}

	if len(scores) == 0 {
		return nil
	}

	results := make([]SearchResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, SearchResult{
			NodeID:     id,
			Similarity: float32(score),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if k > 0 && k < len(results) {
		results = results[:k]
	}

	return results
}

// Len returns the number of indexed documents.
func (idx *BM25Index) Len() int {
	return idx.numDocs
}

// AddPreTokenized indexes a document from pre-computed term frequencies,
// skipping tokenization entirely. Used when restoring from a persisted
// index where tokenization was already done at capture time.
func (idx *BM25Index) AddPreTokenized(nodeID string, termFreqs map[string]int, docLength int) {
	if _, exists := idx.tf[nodeID]; exists {
		idx.removeInternal(nodeID)
	}
	if len(termFreqs) == 0 {
		return
	}

	idx.tf[nodeID] = termFreqs
	idx.docLen[nodeID] = docLength

	for token := range termFreqs {
		if idx.inverted[token] == nil {
			idx.inverted[token] = make(map[string]struct{})
		}
		idx.inverted[token][nodeID] = struct{}{}
	}

	idx.numDocs++
	idx.recomputeAvgDL()
}

// bm25 serialization format (binary, little-endian):
//
//   header:
//     magic     [4]byte  "BM25"
//     version   uint16   1
//     numDocs   uint32
//     k1        float64
//     b         float64
//
//   per document (repeated numDocs times):
//     nodeID_len   uint16
//     nodeID       []byte
//     docLen       uint32
//     numTerms     uint32
//     per term (repeated numTerms times):
//       term_len   uint16
//       term       []byte
//       count      uint32

// MarshalBinary serializes the BM25 index to a compact binary format.
// Persists term frequencies and document lengths per document. The
// inverted index is rebuilt from this data on unmarshal (O(N), no
// tokenization needed).
func (idx *BM25Index) MarshalBinary() ([]byte, error) {
	// Estimate size: header + per-doc overhead.
	buf := make([]byte, 0, 128+idx.numDocs*256)

	// Header.
	buf = append(buf, 'B', 'M', '2', '5')
	buf = binary.LittleEndian.AppendUint16(buf, 1) // version
	buf = binary.LittleEndian.AppendUint32(buf, uint32(idx.numDocs))
	buf = appendFloat64(buf, idx.k1)
	buf = appendFloat64(buf, idx.b)

	// Documents (sorted by nodeID for deterministic output).
	nodeIDs := make([]string, 0, len(idx.tf))
	for id := range idx.tf {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	for _, nodeID := range nodeIDs {
		tf := idx.tf[nodeID]
		dl := idx.docLen[nodeID]

		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(nodeID)))
		buf = append(buf, nodeID...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(dl))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(tf)))

		// Sort terms for determinism.
		terms := make([]string, 0, len(tf))
		for t := range tf {
			terms = append(terms, t)
		}
		sort.Strings(terms)

		for _, term := range terms {
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(term)))
			buf = append(buf, term...)
			buf = binary.LittleEndian.AppendUint32(buf, uint32(tf[term]))
		}
	}

	return buf, nil
}

// UnmarshalBinary restores a BM25 index from binary data produced by
// MarshalBinary. Rebuilds the inverted index from term frequencies
// without re-tokenizing any text.
func (idx *BM25Index) UnmarshalBinary(data []byte) error {
	if len(data) < 26 { // minimum header size
		return fmt.Errorf("bm25: data too short")
	}

	// Header.
	if string(data[:4]) != "BM25" {
		return fmt.Errorf("bm25: invalid magic")
	}
	version := binary.LittleEndian.Uint16(data[4:6])
	if version != 1 {
		return fmt.Errorf("bm25: unsupported version %d", version)
	}
	numDocs := binary.LittleEndian.Uint32(data[6:10])
	if numDocs > 10_000_000 {
		return fmt.Errorf("bm25: numDocs %d exceeds maximum 10000000", numDocs)
	}
	k1 := readFloat64(data[10:18])
	b := readFloat64(data[18:26])

	idx.k1 = k1
	idx.b = b
	idx.tf = make(map[string]map[string]int, numDocs)
	idx.docLen = make(map[string]int, numDocs)
	idx.inverted = make(map[string]map[string]struct{})
	idx.numDocs = 0

	pos := 26
	for i := uint32(0); i < numDocs; i++ {
		if pos+2 > len(data) {
			return fmt.Errorf("bm25: truncated at doc %d", i)
		}
		nodeIDLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+nodeIDLen > len(data) {
			return fmt.Errorf("bm25: truncated nodeID at doc %d", i)
		}
		nodeID := string(data[pos : pos+nodeIDLen])
		pos += nodeIDLen

		if pos+8 > len(data) {
			return fmt.Errorf("bm25: truncated doc header at doc %d", i)
		}
		docLen := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4
		numTerms := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if numTerms > 10_000_000 {
			return fmt.Errorf("bm25: numTerms %d exceeds maximum at doc %d", numTerms, i)
		}

		tf := make(map[string]int, numTerms)
		for j := 0; j < numTerms; j++ {
			if pos+2 > len(data) {
				return fmt.Errorf("bm25: truncated term at doc %d term %d", i, j)
			}
			termLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
			pos += 2
			remaining := len(data) - pos
			if remaining < termLen+4 {
				return fmt.Errorf("bm25: truncated term data at doc %d term %d", i, j)
			}
			term := string(data[pos : pos+termLen])
			pos += termLen
			count := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
			tf[term] = count
		}

		idx.AddPreTokenized(nodeID, tf, docLen)
	}

	return nil
}

func appendFloat64(buf []byte, v float64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	return append(buf, b[:]...)
}

func readFloat64(data []byte) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(data))
}
