package graph

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var (
	edgesBucket  = []byte("edges")
	adjOutBucket = []byte("adj:out")
	adjInBucket  = []byte("adj:in")
	adjTypBucket = []byte("adj:type")
)

// BboltEdgeStore is a disk-backed EdgeStore using bbolt (D25).
//
// Bucket layout:
//
//	edges      -> edge_id -> serialized Edge (MarshalEdge format)
//	adj:out    -> node_id -> encoded edge ID list
//	adj:in     -> node_id -> encoded edge ID list
//	adj:type   -> edge_type -> encoded edge ID list
//
// An LRU cache holds recently accessed edges to avoid repeated
// bbolt reads for hot paths (graph traversal neighborhoods).
//
// Concurrency: Put/Delete take no *bolt.Tx (open their own Update);
// PutTx/DeleteTx accept the caller's tx + an optional *EdgeBatch
// adjacency cache. Removes the stashed-pointer race class.
type BboltEdgeStore struct {
	db    *bolt.DB
	cache *edgeLRU
}

// EdgeBatch bundles the in-batch adjacency cache.
// addToEdgeIDList decoded, linear-scanned, sorted, and re-encoded the
// full edge ID list for each single-item write -- O(K log K) per
// edge with K being the node's current degree. Bulk-loading a node
// with K edges was O(K^2 log K). These maps buffer decoded adjacency
// lists per bucket; FlushBatchTx flushes each dirty key once.
//
// Pass via PutTx/DeleteTx and flush via FlushBatchTx when done. A
// nil *EdgeBatch disables caching and falls back to per-call encode/
// decode.
type EdgeBatch struct {
	adjOut map[string][]string
	adjIn  map[string][]string
	adjTyp map[string][]string
}

// NewEdgeBatch creates an empty adjacency cache for use with a
// single shared bbolt transaction. The caller flushes via
// FlushBatchTx.
func NewEdgeBatch() *EdgeBatch {
	return &EdgeBatch{
		adjOut: make(map[string][]string),
		adjIn:  make(map[string][]string),
		adjTyp: make(map[string][]string),
	}
}

// DefaultEdgeCacheCapacity is the default max edges in the LRU cache.
const DefaultEdgeCacheCapacity = 10000

// NewBboltEdgeStore opens or creates a bbolt-backed edge store.
func NewBboltEdgeStore(db *bolt.DB, cacheCapacity int) (*BboltEdgeStore, error) {
	if cacheCapacity <= 0 {
		cacheCapacity = DefaultEdgeCacheCapacity
	}
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{edgesBucket, adjOutBucket, adjInBucket, adjTypBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt edge store: create buckets: %w", err)
	}
	return &BboltEdgeStore{
		db:    db,
		cache: newEdgeLRU(cacheCapacity),
	}, nil
}

// FlushBatchTx writes the *EdgeBatch's cached adjacency lists back
// to bbolt via tx. Safe with nil batch (no-op).
func (s *BboltEdgeStore) FlushBatchTx(tx *bolt.Tx, batch *EdgeBatch) {
	if batch == nil {
		return
	}
	flushAdj := func(bucket []byte, cache map[string][]string) {
		b := tx.Bucket(bucket)
		for key, ids := range cache {
			k := []byte(key)
			if len(ids) == 0 {
				b.Delete(k)
			} else {
				b.Put(k, encodeEdgeIDList(ids))
			}
		}
	}
	flushAdj(adjOutBucket, batch.adjOut)
	flushAdj(adjInBucket, batch.adjIn)
	flushAdj(adjTypBucket, batch.adjTyp)
}

// pickBatchCache returns the batch map for a given adjacency bucket,
// or nil if batch is nil.
func pickBatchCache(batch *EdgeBatch, bucket []byte) map[string][]string {
	if batch == nil {
		return nil
	}
	switch string(bucket) {
	case string(adjOutBucket):
		return batch.adjOut
	case string(adjInBucket):
		return batch.adjIn
	case string(adjTypBucket):
		return batch.adjTyp
	}
	return nil
}

// Put stores an edge via its own bbolt Update.
func (s *BboltEdgeStore) Put(e *Edge) {
	s.cache.Put(e)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return s.putInTx(tx, nil, e)
	}); err != nil {
		slog.Error("bbolt edge store: put", "edge", e.ID, "err", err)
	}
}

// PutTx stores an edge via the caller's tx + optional *EdgeBatch.
func (s *BboltEdgeStore) PutTx(tx *bolt.Tx, batch *EdgeBatch, e *Edge) {
	s.cache.Put(e)
	if err := s.putInTx(tx, batch, e); err != nil {
		slog.Error("bbolt edge store: put (tx)", "edge", e.ID, "err", err)
	}
}

func (s *BboltEdgeStore) putInTx(tx *bolt.Tx, batch *EdgeBatch, e *Edge) error {
	data, err := MarshalEdge(e)
	if err != nil {
		return fmt.Errorf("marshal edge %s: %w", e.ID, err)
	}
	if err := tx.Bucket(edgesBucket).Put([]byte(e.ID), data); err != nil {
		return err
	}
	s.addToAdj(tx.Bucket(adjOutBucket), batch, adjOutBucket, e.SourceID, e.ID)
	s.addToAdj(tx.Bucket(adjInBucket), batch, adjInBucket, e.TargetID, e.ID)
	s.addToAdj(tx.Bucket(adjTypBucket), batch, adjTypBucket, e.Type, e.ID)
	return nil
}

func (s *BboltEdgeStore) Get(id string) (*Edge, bool) {
	if e, ok := s.cache.Get(id); ok {
		return e, true
	}
	var e *Edge
	if err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(edgesBucket).Get([]byte(id))
		if data == nil {
			return nil
		}
		var err error
		e, err = UnmarshalEdge(data)
		if err != nil {
			slog.Error("bbolt edge store: unmarshal", "edge", id, "err", err)
			return nil
		}
		return nil
	}); err != nil {
		slog.Warn("bbolt edge store: get view failed", "edge", id, "err", err)
		return nil, false
	}
	if e != nil {
		s.cache.Put(e)
		return e, true
	}
	return nil, false
}

// Delete removes an edge via its own tx.
func (s *BboltEdgeStore) Delete(id string) {
	// Get the edge first to update adjacency indexes.
	e, ok := s.Get(id)
	if !ok {
		return
	}
	s.cache.Remove(id)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		s.deleteInTx(tx, nil, e)
		return nil
	}); err != nil {
		slog.Error("bbolt edge store: delete", "edge", id, "err", err)
	}
}

// DeleteTx removes an edge via the caller's tx + optional *EdgeBatch.
func (s *BboltEdgeStore) DeleteTx(tx *bolt.Tx, batch *EdgeBatch, id string) {
	e, ok := s.Get(id)
	if !ok {
		return
	}
	s.cache.Remove(id)
	s.deleteInTx(tx, batch, e)
}

func (s *BboltEdgeStore) deleteInTx(tx *bolt.Tx, batch *EdgeBatch, e *Edge) {
	tx.Bucket(edgesBucket).Delete([]byte(e.ID))
	s.removeFromAdj(tx.Bucket(adjOutBucket), batch, adjOutBucket, e.SourceID, e.ID)
	s.removeFromAdj(tx.Bucket(adjInBucket), batch, adjInBucket, e.TargetID, e.ID)
	s.removeFromAdj(tx.Bucket(adjTypBucket), batch, adjTypBucket, e.Type, e.ID)
}

func (s *BboltEdgeStore) From(nodeID string) []*Edge {
	return s.loadEdgesFromBucket(adjOutBucket, nodeID)
}

func (s *BboltEdgeStore) To(nodeID string) []*Edge {
	return s.loadEdgesFromBucket(adjInBucket, nodeID)
}

func (s *BboltEdgeStore) ByType(edgeType string) []*Edge {
	return s.loadEdgesFromBucket(adjTypBucket, edgeType)
}

func (s *BboltEdgeStore) Count() int {
	count := 0
	if err := s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(edgesBucket).Stats().KeyN
		return nil
	}); err != nil {
		slog.Warn("bbolt edge store: count view failed", "err", err)
	}
	return count
}

func (s *BboltEdgeStore) ForEach(fn func(e *Edge)) {
	if err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(edgesBucket).ForEach(func(k, v []byte) error {
			e, err := UnmarshalEdge(v)
			if err != nil {
				slog.Error("bbolt edge store: foreach unmarshal", "err", err)
				return nil
			}
			fn(e)
			return nil
		})
	}); err != nil {
		slog.Warn("bbolt edge store: foreach view failed", "err", err)
	}
}

// loadEdgesFromBucket reads the edge-ID list for key from the
// adjacency bucket and returns the corresponding edges. Lookups
// for cached edges short-circuit; uncached edges are batched into
// a single bbolt View instead of one View per edge. Previously
// each cache miss opened its own transaction, making EdgesFrom
// cost O(N+1) views on a hot path.
func (s *BboltEdgeStore) loadEdgesFromBucket(bucket []byte, key string) []*Edge {
	var edgeIDs []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		edgeIDs = decodeEdgeIDList(tx.Bucket(bucket).Get([]byte(key)))
		return nil
	}); err != nil {
		slog.Warn("bbolt edge store: loadEdges view failed",
			"bucket", string(bucket), "key", key, "err", err)
		return nil
	}
	if len(edgeIDs) == 0 {
		return nil
	}

	// Edges are returned in the bucket's stored order. We walk the
	// list once, collecting cache hits inline and queueing misses
	// for a batched View so the order on the way out matches.
	edges := make([]*Edge, 0, len(edgeIDs))
	type missSlot struct {
		idx int
		id  string
	}
	var misses []missSlot
	for _, eid := range edgeIDs {
		if e, ok := s.cache.Get(eid); ok {
			edges = append(edges, e)
			continue
		}
		// Reserve the slot; we'll fill it in after the batched read.
		misses = append(misses, missSlot{idx: len(edges), id: eid})
		edges = append(edges, nil)
	}

	if len(misses) > 0 {
		_ = s.db.View(func(tx *bolt.Tx) error {
			eb := tx.Bucket(edgesBucket)
			for _, m := range misses {
				data := eb.Get([]byte(m.id))
				if data == nil {
					continue
				}
				e, err := UnmarshalEdge(data)
				if err != nil {
					slog.Error("bbolt edge store: loadEdges unmarshal",
						"edge", m.id, "err", err)
					continue
				}
				s.cache.Put(e)
				edges[m.idx] = e
			}
			return nil
		})
	}

	// Compact: drop nil slots from edges whose row was missing on disk
	// (data races, manual deletion). Caller expects a tight slice.
	out := edges[:0]
	for _, e := range edges {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

// --- Edge ID list encoding ---
//
// A list of edge ID strings stored as:
//   uint32(count) + for each: uint16(len) + []byte(id)

func encodeEdgeIDList(ids []string) []byte {
	sort.Strings(ids)
	size := 4
	for _, id := range ids {
		size += 2 + len(id)
	}
	buf := make([]byte, 0, size)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(ids)))
	for _, id := range ids {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(id)))
		buf = append(buf, id...)
	}
	return buf
}

func decodeEdgeIDList(data []byte) []string {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[:4]))
	if count == 0 {
		return nil
	}
	ids := make([]string, 0, count)
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
		ids = append(ids, string(data[pos:pos+l]))
		pos += l
	}
	return ids
}

// addToAdj inserts an edge id into the adjacency list for (bucket, key).
// Routes through the batch cache when batch is non-nil so a node's K
// edge inserts pay O(K) total instead of O(K^2 log K) per-write encode
// cycles.
func (s *BboltEdgeStore) addToAdj(b *bolt.Bucket, batch *EdgeBatch, bucket []byte, key, id string) {
	cache := pickBatchCache(batch, bucket)
	if cache != nil {
		existing, ok := cache[key]
		if !ok {
			existing = decodeEdgeIDList(b.Get([]byte(key)))
		}
		for _, eid := range existing {
			if eid == id {
				cache[key] = existing
				return
			}
		}
		cache[key] = append(existing, id)
		return
	}
	existing := decodeEdgeIDList(b.Get([]byte(key)))
	for _, eid := range existing {
		if eid == id {
			return
		}
	}
	existing = append(existing, id)
	b.Put([]byte(key), encodeEdgeIDList(existing))
}

func (s *BboltEdgeStore) removeFromAdj(b *bolt.Bucket, batch *EdgeBatch, bucket []byte, key, id string) {
	cache := pickBatchCache(batch, bucket)
	if cache != nil {
		existing, ok := cache[key]
		if !ok {
			existing = decodeEdgeIDList(b.Get([]byte(key)))
		}
		for i, eid := range existing {
			if eid == id {
				existing = append(existing[:i], existing[i+1:]...)
				cache[key] = existing
				return
			}
		}
		// Unchanged: still cache it so subsequent reads in this batch
		// don't re-decode from bbolt.
		cache[key] = existing
		return
	}
	existing := decodeEdgeIDList(b.Get([]byte(key)))
	for i, eid := range existing {
		if eid == id {
			existing = append(existing[:i], existing[i+1:]...)
			if len(existing) == 0 {
				b.Delete([]byte(key))
			} else {
				b.Put([]byte(key), encodeEdgeIDList(existing))
			}
			return
		}
	}
}

// --- Simple edge LRU cache ---

type edgeLRU struct {
	mu       sync.Mutex
	capacity int
	edges    map[string]*Edge
	order    []string // most recent at end
}

func newEdgeLRU(capacity int) *edgeLRU {
	return &edgeLRU{
		capacity: capacity,
		edges:    make(map[string]*Edge, capacity),
	}
}

func (c *edgeLRU) Get(id string) (*Edge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.edges[id]
	if ok {
		c.touch(id)
	}
	return e, ok
}

func (c *edgeLRU) Put(e *Edge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.edges[e.ID]; exists {
		c.edges[e.ID] = e
		c.touch(e.ID)
		return
	}
	if len(c.edges) >= c.capacity {
		c.evict()
	}
	c.edges[e.ID] = e
	c.order = append(c.order, e.ID)
}

func (c *edgeLRU) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.edges, id)
	for i, eid := range c.order {
		if eid == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *edgeLRU) touch(id string) {
	for i, eid := range c.order {
		if eid == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, id)
			return
		}
	}
}

func (c *edgeLRU) evict() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.edges, oldest)
}

// reset empties the cache in place. Used by BboltEdgeStore.Clear so
// the cache field doesn't have to be reassigned (which would race
// with concurrent Get callers reading the old pointer).
func (c *edgeLRU) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edges = make(map[string]*Edge, c.capacity)
	c.order = nil
}

// Clear deletes every edge bucket and empties the in-memory cache.
//
// Caller is expected to hold the engine write lock so no concurrent
// Get/Put hits the cache during the swap. Even so, the cache is
// emptied in place via reset() instead of pointer-reassigning the
// cache field -- the latter would race with Get callers that
// snapshot the field before the swap.
func (s *BboltEdgeStore) Clear() {
	s.cache.reset()
	if err := s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{edgesBucket, adjOutBucket, adjInBucket, adjTypBucket} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Error("bbolt edge store: clear", "err", err)
	}
}

// Verify interface compliance.
var _ EdgeStore = (*BboltEdgeStore)(nil)
