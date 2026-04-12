package index

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// BboltBM25Index is a disk-backed BM25 implementation using bbolt.
//
// Bucket layout:
//
//	bm25:postings  -> term -> encoded posting list [(docID, TF), ...]
//	bm25:doclen    -> docID -> uint32 document length (total token count)
//	bm25:meta      -> "num_docs" -> uint32
//	                  "total_len" -> uint64 (sum of all doc lengths)
//
// Each posting list entry: uint16(idLen) + []byte(docID) + uint32(TF).
// Posting lists are sorted by docID for determinism.
//
// BM25 scoring uses the Okapi BM25 formula with configurable k1 and b.
type BboltBM25Index struct {
	db *bolt.DB
	k1 float64
	b  float64

	// Cached metadata to avoid bbolt reads on every Search.
	numDocs  int
	totalLen uint64  // sum of all doc lengths, maintained incrementally
	avgDL    float64
	batch    *bolt.Tx // non-nil during Batch() call
}

var (
	bm25PostingsBucket = []byte("bm25:postings")
	bm25DoclenBucket   = []byte("bm25:doclen")
	bm25ReverseBucket  = []byte("bm25:reverse") // nodeID -> list of terms (for efficient Remove)
	bm25MetaBucket     = []byte("bm25:meta")
)

// NewBboltBM25Index opens or creates a bbolt-backed BM25 index.
func NewBboltBM25Index(db *bolt.DB, k1, b float64) (*BboltBM25Index, error) {
	if k1 == 0 {
		k1 = 1.2
	}
	if b == 0 {
		b = 0.75
	}

	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bm25PostingsBucket, bm25DoclenBucket, bm25ReverseBucket, bm25MetaBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt bm25: create buckets: %w", err)
	}

	idx := &BboltBM25Index{db: db, k1: k1, b: b}
	idx.loadMeta()
	return idx, nil
}

func (idx *BboltBM25Index) loadMeta() {
	idx.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bm25MetaBucket)
		if v := mb.Get([]byte("num_docs")); len(v) >= 4 {
			idx.numDocs = int(binary.LittleEndian.Uint32(v))
		}
		if v := mb.Get([]byte("total_len")); len(v) >= 8 {
			idx.totalLen = binary.LittleEndian.Uint64(v)
		}
		if idx.numDocs > 0 {
			idx.avgDL = float64(idx.totalLen) / float64(idx.numDocs)
		}
		return nil
	})
}

func (idx *BboltBM25Index) saveMeta(tx *bolt.Tx) {
	mb := tx.Bucket(bm25MetaBucket)
	var buf4 [4]byte
	binary.LittleEndian.PutUint32(buf4[:], uint32(idx.numDocs))
	mb.Put([]byte("num_docs"), buf4[:])
	var buf8 [8]byte
	binary.LittleEndian.PutUint64(buf8[:], idx.totalLen)
	mb.Put([]byte("total_len"), buf8[:])
	if idx.numDocs > 0 {
		idx.avgDL = float64(idx.totalLen) / float64(idx.numDocs)
	} else {
		idx.avgDL = 0
	}
}

func (idx *BboltBM25Index) update(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.Update(fn)
}

func (idx *BboltBM25Index) view(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.View(fn)
}

// Batch executes fn within a single bbolt write transaction.
func (idx *BboltBM25Index) Batch(fn func()) error {
	if idx.batch != nil {
		fn()
		return nil
	}
	return idx.db.Update(func(tx *bolt.Tx) error {
		idx.batch = tx
		defer func() { idx.batch = nil }()
		fn()
		return nil
	})
}

// SetBatch sets an external bbolt transaction for batching.
func (idx *BboltBM25Index) SetBatch(tx *bolt.Tx) { idx.batch = tx }

// ClearBatch clears the external batch transaction.
func (idx *BboltBM25Index) ClearBatch() { idx.batch = nil }

func (idx *BboltBM25Index) Add(nodeID, text string) {
	tokens := Tokenize(text)
	if len(tokens) == 0 {
		return
	}
	tf := make(map[string]int, len(tokens)/2)
	for _, t := range tokens {
		tf[t]++
	}
	idx.AddPreTokenized(nodeID, tf, len(tokens))
}

func (idx *BboltBM25Index) AddPreTokenized(nodeID string, termFreqs map[string]int, docLength int) {
	if len(termFreqs) == 0 {
		return
	}

	if err := idx.update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bm25PostingsBucket)
		db := tx.Bucket(bm25DoclenBucket)
		rb := tx.Bucket(bm25ReverseBucket)

		// Remove old entry if exists (using reverse index for efficiency).
		if oldLenBytes := db.Get([]byte(nodeID)); oldLenBytes != nil {
			oldLen := int(binary.LittleEndian.Uint32(oldLenBytes))
			idx.removeFromPostingsViaReverse(tx, nodeID)
			idx.numDocs--
			idx.totalLen -= uint64(oldLen)
		}

		// Add to posting lists.
		terms := make([]string, 0, len(termFreqs))
		for term, count := range termFreqs {
			addToPostingList(pb, []byte(term), nodeID, count)
			terms = append(terms, term)
		}

		// Store reverse index (nodeID -> list of terms).
		rb.Put([]byte(nodeID), encodeTermList(terms))

		// Store doc length.
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(docLength))
		db.Put([]byte(nodeID), buf[:])

		idx.numDocs++
		idx.totalLen += uint64(docLength)
		idx.saveMeta(tx)

		return nil
	}); err != nil {
		slog.Error("bbolt bm25: add", "node", nodeID, "err", err)
	}
}

func (idx *BboltBM25Index) Remove(nodeID string) {
	if err := idx.update(func(tx *bolt.Tx) error {
		db := tx.Bucket(bm25DoclenBucket)
		oldLenBytes := db.Get([]byte(nodeID))
		if oldLenBytes == nil {
			return nil // not indexed
		}
		oldLen := int(binary.LittleEndian.Uint32(oldLenBytes))

		idx.removeFromPostingsViaReverse(tx, nodeID)
		db.Delete([]byte(nodeID))
		tx.Bucket(bm25ReverseBucket).Delete([]byte(nodeID))
		idx.numDocs--
		idx.totalLen -= uint64(oldLen)
		idx.saveMeta(tx)

		return nil
	}); err != nil {
		slog.Error("bbolt bm25: remove", "node", nodeID, "err", err)
	}
}

// removeFromPostingsViaReverse uses the reverse index (nodeID -> terms)
// to efficiently remove a node from only the posting lists it appears in.
// O(terms_per_doc) instead of O(vocabulary).
func (idx *BboltBM25Index) removeFromPostingsViaReverse(tx *bolt.Tx, nodeID string) {
	rb := tx.Bucket(bm25ReverseBucket)
	pb := tx.Bucket(bm25PostingsBucket)

	terms := decodeTermList(rb.Get([]byte(nodeID)))
	for _, term := range terms {
		key := []byte(term)
		v := pb.Get(key)
		newV := removeFromPostingList(v, nodeID)
		if newV == nil {
			pb.Delete(key)
		} else if len(newV) != len(v) {
			pb.Put(key, newV)
		}
	}
}

func (idx *BboltBM25Index) Search(queryTokens []string, k int, candidates map[string]struct{}) []SearchResult {
	if len(queryTokens) == 0 || idx.numDocs == 0 || k <= 0 || idx.avgDL == 0 {
		return nil
	}

	// Deduplicate query tokens.
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, t := range queryTokens {
		querySet[t] = struct{}{}
	}

	scores := make(map[string]float64)

	idx.view(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bm25PostingsBucket)
		db := tx.Bucket(bm25DoclenBucket)

		for token := range querySet {
			entries := decodePostingList(pb.Get([]byte(token)))
			if len(entries) == 0 {
				continue
			}

			// IDF: log((N - n + 0.5) / (n + 0.5) + 1)
			n := float64(len(entries))
			idf := math.Log((float64(idx.numDocs)-n+0.5)/(n+0.5) + 1)

			for _, pe := range entries {
				if candidates != nil {
					if _, ok := candidates[pe.nodeID]; !ok {
						continue
					}
				}

				tf := float64(pe.tf)
				dl := float64(idx.getDocLen(db, pe.nodeID))

				// BM25 score.
				denom := tf + idx.k1*(1-idx.b+idx.b*dl/idx.avgDL)
				score := idf * (tf * (idx.k1 + 1)) / denom
				scores[pe.nodeID] += score
			}
		}
		return nil
	})

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
	if k < len(results) {
		results = results[:k]
	}
	return results
}

func (idx *BboltBM25Index) Len() int {
	return idx.numDocs
}

func (idx *BboltBM25Index) getDocLen(db *bolt.Bucket, nodeID string) int {
	v := db.Get([]byte(nodeID))
	if len(v) < 4 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(v))
}

// --- Term list encoding (for reverse index) ---
//
// A list of term strings: uint32(count) + for each: uint16(len) + []byte(term)

func encodeTermList(terms []string) []byte {
	size := 4
	for _, t := range terms {
		size += 2 + len(t)
	}
	buf := make([]byte, 0, size)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(terms)))
	for _, t := range terms {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(t)))
		buf = append(buf, t...)
	}
	return buf
}

func decodeTermList(data []byte) []string {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[:4]))
	terms := make([]string, 0, count)
	pos := 4
	for i := 0; i < count; i++ {
		if pos+2 > len(data) {
			break
		}
		l := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+l > len(data) {
			break
		}
		terms = append(terms, string(data[pos:pos+l]))
		pos += l
	}
	return terms
}

// --- Posting list encoding ---
//
// A posting list is a sorted sequence of (docID, TF) entries:
//   uint32(count) + for each: uint16(idLen) + []byte(docID) + uint32(tf)

type postingEntry struct {
	nodeID string
	tf     int
}

func encodePostingList(entries []postingEntry) []byte {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].nodeID < entries[j].nodeID
	})
	size := 4
	for _, e := range entries {
		size += 2 + len(e.nodeID) + 4
	}
	buf := make([]byte, 0, size)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(entries)))
	for _, e := range entries {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.nodeID)))
		buf = append(buf, e.nodeID...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(e.tf))
	}
	return buf
}

func decodePostingList(data []byte) []postingEntry {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[:4]))
	if count == 0 {
		return nil
	}
	entries := make([]postingEntry, 0, count)
	pos := 4
	for i := 0; i < count; i++ {
		if pos+2 > len(data) {
			break
		}
		idLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+idLen+4 > len(data) {
			break
		}
		nodeID := string(data[pos : pos+idLen])
		pos += idLen
		tf := int(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
		entries = append(entries, postingEntry{nodeID: nodeID, tf: tf})
	}
	return entries
}

func addToPostingList(b *bolt.Bucket, term []byte, nodeID string, tf int) {
	entries := decodePostingList(b.Get(term))
	// Check for duplicate.
	for i, e := range entries {
		if e.nodeID == nodeID {
			entries[i].tf = tf
			b.Put(term, encodePostingList(entries))
			return
		}
	}
	entries = append(entries, postingEntry{nodeID: nodeID, tf: tf})
	b.Put(term, encodePostingList(entries))
}

func removeFromPostingList(data []byte, nodeID string) []byte {
	entries := decodePostingList(data)
	for i, e := range entries {
		if e.nodeID == nodeID {
			entries = append(entries[:i], entries[i+1:]...)
			if len(entries) == 0 {
				return nil
			}
			return encodePostingList(entries)
		}
	}
	return data // not found, return unchanged
}

// Verify interface compliance.
var _ BM25Index = (*BboltBM25Index)(nil)
