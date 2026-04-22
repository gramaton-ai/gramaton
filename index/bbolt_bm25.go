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
//
// Concurrency: The numDocs/totalLen/avgDL Go fields mutate without
// internal locking; this type relies on the engine's RWMutex
// serialising every caller. Mutating methods take an explicit
// *bolt.Tx; the in-batch write cache is a *BM25Batch passed as a
// parameter (nil disables caching and falls back to per-call
// encode/decode). Removes the P2-06 stashed-pointer race class.
type BboltBM25Index struct {
	db *bolt.DB
	k1 float64
	b  float64

	// Cached metadata to avoid bbolt reads on every Search.
	numDocs  int
	totalLen uint64 // sum of all doc lengths, maintained incrementally
	avgDL    float64
}

// BM25Batch bundles the in-batch write cache. During a batch,
// every addToPostingList previously had to decode -> linear-scan ->
// sort -> encode the full posting list for each single-item write.
// A common term like "the" with K postings produces O(K log K) work
// per insert; N bulk inserts cost O(N*K*log K).
//
// These maps buffer decoded state. Reads prefer the cache, mutations
// stay in the cache, and FlushBatchTx encodes+writes each dirty term
// once. O(N*K_final*log K_final) total.
//
// Pass via AddTx/RemoveTx and flush via FlushBatchTx when done. A nil
// *BM25Batch disables caching and falls back to per-call encode/decode.
type BM25Batch struct {
	postings map[string][]postingEntry
	reverse  map[string][]string // nodeID -> list of terms
}

// NewBM25Batch creates an empty batch cache for use with a single
// shared bbolt transaction. The caller flushes via FlushBatchTx.
func NewBM25Batch() *BM25Batch {
	return &BM25Batch{
		postings: make(map[string][]postingEntry),
		reverse:  make(map[string][]string),
	}
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
	if err := idx.loadMeta(); err != nil {
		return nil, fmt.Errorf("bbolt bm25: load meta: %w", err)
	}
	return idx, nil
}

// loadMeta restores numDocs/totalLen/avgDL from the on-disk meta
// bucket. If this fails silently and avgDL stays 0, every Search
// short-circuits at the avgDL==0 guard and returns nil -- a
// catastrophic but invisible bug.
func (idx *BboltBM25Index) loadMeta() error {
	return idx.db.View(func(tx *bolt.Tx) error {
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

// FlushBatchTx writes the *BM25Batch's cached postings and reverse
// terms back to bbolt via tx. Safe with nil batch (no-op).
func (idx *BboltBM25Index) FlushBatchTx(tx *bolt.Tx, batch *BM25Batch) {
	if batch == nil {
		return
	}
	pb := tx.Bucket(bm25PostingsBucket)
	rb := tx.Bucket(bm25ReverseBucket)
	for term, entries := range batch.postings {
		key := []byte(term)
		if len(entries) == 0 {
			pb.Delete(key)
		} else {
			pb.Put(key, encodePostingList(entries))
		}
	}
	for nodeID, terms := range batch.reverse {
		key := []byte(nodeID)
		if len(terms) == 0 {
			rb.Delete(key)
		} else {
			rb.Put(key, encodeTermList(terms))
		}
	}
}

// getPostings returns the current postings for a term, preferring the
// batch cache when non-nil. Decoded-in-bbolt postings are hoisted
// into the cache on first touch so subsequent writes don't re-decode.
func (idx *BboltBM25Index) getPostings(pb *bolt.Bucket, batch *BM25Batch, term string) []postingEntry {
	if batch != nil {
		if entries, ok := batch.postings[term]; ok {
			return entries
		}
		entries := decodePostingList(pb.Get([]byte(term)))
		batch.postings[term] = entries
		return entries
	}
	return decodePostingList(pb.Get([]byte(term)))
}

// setPostings writes postings for a term. During a batch this only
// touches the cache; FlushBatchTx encodes + writes to bbolt.
func (idx *BboltBM25Index) setPostings(pb *bolt.Bucket, batch *BM25Batch, term string, entries []postingEntry) {
	if batch != nil {
		batch.postings[term] = entries
		return
	}
	key := []byte(term)
	if len(entries) == 0 {
		pb.Delete(key)
	} else {
		pb.Put(key, encodePostingList(entries))
	}
}

// getReverseTerms returns the term list for a node, preferring the
// batch cache when non-nil.
func (idx *BboltBM25Index) getReverseTerms(rb *bolt.Bucket, batch *BM25Batch, nodeID string) []string {
	if batch != nil {
		if terms, ok := batch.reverse[nodeID]; ok {
			return terms
		}
		terms := decodeTermList(rb.Get([]byte(nodeID)))
		batch.reverse[nodeID] = terms
		return terms
	}
	return decodeTermList(rb.Get([]byte(nodeID)))
}

// setReverseTerms writes the term list for a node. During a batch
// this only touches the cache.
func (idx *BboltBM25Index) setReverseTerms(rb *bolt.Bucket, batch *BM25Batch, nodeID string, terms []string) {
	if batch != nil {
		batch.reverse[nodeID] = terms
		return
	}
	key := []byte(nodeID)
	if len(terms) == 0 {
		rb.Delete(key)
	} else {
		rb.Put(key, encodeTermList(terms))
	}
}

// Add tokenises text and writes via its own bbolt Update. Use AddTx
// inside a shared transaction.
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

// AddTx tokenises text and writes via the caller's tx. batch may be
// nil (no caching) or an active *BM25Batch to amortize decode/encode.
func (idx *BboltBM25Index) AddTx(tx *bolt.Tx, batch *BM25Batch, nodeID, text string) {
	tokens := Tokenize(text)
	if len(tokens) == 0 {
		return
	}
	tf := make(map[string]int, len(tokens)/2)
	for _, t := range tokens {
		tf[t]++
	}
	idx.AddPreTokenizedTx(tx, batch, nodeID, tf, len(tokens))
}

// AddPreTokenized indexes a document from pre-computed term frequencies
// via its own tx.
func (idx *BboltBM25Index) AddPreTokenized(nodeID string, termFreqs map[string]int, docLength int) {
	if len(termFreqs) == 0 {
		return
	}
	if err := idx.db.Update(func(tx *bolt.Tx) error {
		idx.AddPreTokenizedTx(tx, nil, nodeID, termFreqs, docLength)
		return nil
	}); err != nil {
		slog.Error("bbolt bm25: add", "node", nodeID, "err", err)
	}
}

// AddPreTokenizedTx is AddPreTokenized via the caller's tx + optional
// batch cache.
func (idx *BboltBM25Index) AddPreTokenizedTx(tx *bolt.Tx, batch *BM25Batch, nodeID string, termFreqs map[string]int, docLength int) {
	if len(termFreqs) == 0 {
		return
	}
	pb := tx.Bucket(bm25PostingsBucket)
	db := tx.Bucket(bm25DoclenBucket)
	rb := tx.Bucket(bm25ReverseBucket)

	// Remove old entry if exists (using reverse index for efficiency).
	if oldLenBytes := db.Get([]byte(nodeID)); oldLenBytes != nil {
		oldLen := int(binary.LittleEndian.Uint32(oldLenBytes))
		idx.removeFromPostingsViaReverse(tx, batch, nodeID)
		idx.numDocs--
		idx.totalLen -= uint64(oldLen)
	}

	// Add to posting lists.
	terms := make([]string, 0, len(termFreqs))
	for term, count := range termFreqs {
		idx.addToPostings(pb, batch, term, nodeID, count)
		terms = append(terms, term)
	}

	// Store reverse index (nodeID -> list of terms).
	idx.setReverseTerms(rb, batch, nodeID, terms)

	// Store doc length.
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(docLength))
	db.Put([]byte(nodeID), buf[:])

	idx.numDocs++
	idx.totalLen += uint64(docLength)
	idx.saveMeta(tx)
}

// Remove drops a node via its own tx.
func (idx *BboltBM25Index) Remove(nodeID string) {
	if err := idx.db.Update(func(tx *bolt.Tx) error {
		idx.RemoveTx(tx, nil, nodeID)
		return nil
	}); err != nil {
		slog.Error("bbolt bm25: remove", "node", nodeID, "err", err)
	}
}

// RemoveTx drops a node via the caller's tx. batch may be nil.
func (idx *BboltBM25Index) RemoveTx(tx *bolt.Tx, batch *BM25Batch, nodeID string) {
	db := tx.Bucket(bm25DoclenBucket)
	oldLenBytes := db.Get([]byte(nodeID))
	if oldLenBytes == nil {
		return // not indexed
	}
	oldLen := int(binary.LittleEndian.Uint32(oldLenBytes))

	idx.removeFromPostingsViaReverse(tx, batch, nodeID)
	db.Delete([]byte(nodeID))
	idx.setReverseTerms(tx.Bucket(bm25ReverseBucket), batch, nodeID, nil)
	idx.numDocs--
	idx.totalLen -= uint64(oldLen)
	idx.saveMeta(tx)
}

// removeFromPostingsViaReverse uses the reverse index (nodeID -> terms)
// to efficiently remove a node from only the posting lists it appears in.
// O(terms_per_doc) instead of O(vocabulary). During a batch, writes go
// through the posting-list cache rather than the per-write encode cycle.
func (idx *BboltBM25Index) removeFromPostingsViaReverse(tx *bolt.Tx, batch *BM25Batch, nodeID string) {
	rb := tx.Bucket(bm25ReverseBucket)
	pb := tx.Bucket(bm25PostingsBucket)

	terms := idx.getReverseTerms(rb, batch, nodeID)
	for _, term := range terms {
		entries := idx.getPostings(pb, batch, term)
		for i, e := range entries {
			if e.nodeID == nodeID {
				entries = append(entries[:i], entries[i+1:]...)
				idx.setPostings(pb, batch, term, entries)
				break
			}
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

	idx.db.View(func(tx *bolt.Tx) error {
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
	// Sort a copy so callers don't observe in-place mutation. The
	// in-batch cache reuses entries slices across flushes; sorting
	// the original would break iteration order assumptions elsewhere.
	sorted := append([]postingEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].nodeID < sorted[j].nodeID
	})
	entries = sorted
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

// addToPostings inserts or updates a (nodeID, tf) in the posting list
// for term. Routes through the batch cache when batching so N bulk
// inserts pay O(N) total instead of O(N*K log K) per-write.
func (idx *BboltBM25Index) addToPostings(pb *bolt.Bucket, batch *BM25Batch, term, nodeID string, tf int) {
	entries := idx.getPostings(pb, batch, term)
	for i, e := range entries {
		if e.nodeID == nodeID {
			entries[i].tf = tf
			idx.setPostings(pb, batch, term, entries)
			return
		}
	}
	entries = append(entries, postingEntry{nodeID: nodeID, tf: tf})
	idx.setPostings(pb, batch, term, entries)
}

// Verify interface compliance.
var _ BM25Index = (*BboltBM25Index)(nil)
