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
type BboltEdgeStore struct {
	db    *bolt.DB
	cache *edgeLRU
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

func (s *BboltEdgeStore) Put(e *Edge) {
	s.cache.Put(e)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		data, err := MarshalEdge(e)
		if err != nil {
			return fmt.Errorf("marshal edge %s: %w", e.ID, err)
		}
		if err := tx.Bucket(edgesBucket).Put([]byte(e.ID), data); err != nil {
			return err
		}
		addToEdgeIDList(tx.Bucket(adjOutBucket), []byte(e.SourceID), e.ID)
		addToEdgeIDList(tx.Bucket(adjInBucket), []byte(e.TargetID), e.ID)
		addToEdgeIDList(tx.Bucket(adjTypBucket), []byte(e.Type), e.ID)
		return nil
	}); err != nil {
		slog.Error("bbolt edge store: put", "edge", e.ID, "err", err)
	}
}

func (s *BboltEdgeStore) Get(id string) (*Edge, bool) {
	if e, ok := s.cache.Get(id); ok {
		return e, true
	}
	var e *Edge
	s.db.View(func(tx *bolt.Tx) error {
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
	})
	if e != nil {
		s.cache.Put(e)
		return e, true
	}
	return nil, false
}

func (s *BboltEdgeStore) Delete(id string) {
	// Get the edge first to update adjacency indexes.
	e, ok := s.Get(id)
	if !ok {
		return
	}
	s.cache.Remove(id)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		tx.Bucket(edgesBucket).Delete([]byte(id))
		removeFromEdgeIDList(tx.Bucket(adjOutBucket), []byte(e.SourceID), id)
		removeFromEdgeIDList(tx.Bucket(adjInBucket), []byte(e.TargetID), id)
		removeFromEdgeIDList(tx.Bucket(adjTypBucket), []byte(e.Type), id)
		return nil
	}); err != nil {
		slog.Error("bbolt edge store: delete", "edge", id, "err", err)
	}
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
	s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(edgesBucket).Stats().KeyN
		return nil
	})
	return count
}

func (s *BboltEdgeStore) ForEach(fn func(e *Edge)) {
	s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(edgesBucket).ForEach(func(k, v []byte) error {
			e, err := UnmarshalEdge(v)
			if err != nil {
				slog.Error("bbolt edge store: foreach unmarshal", "err", err)
				return nil
			}
			fn(e)
			return nil
		})
	})
}

func (s *BboltEdgeStore) loadEdgesFromBucket(bucket []byte, key string) []*Edge {
	var edgeIDs []string
	s.db.View(func(tx *bolt.Tx) error {
		edgeIDs = decodeEdgeIDList(tx.Bucket(bucket).Get([]byte(key)))
		return nil
	})
	if len(edgeIDs) == 0 {
		return nil
	}
	edges := make([]*Edge, 0, len(edgeIDs))
	for _, eid := range edgeIDs {
		if e, ok := s.Get(eid); ok {
			edges = append(edges, e)
		}
	}
	return edges
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

func addToEdgeIDList(b *bolt.Bucket, key []byte, id string) {
	existing := decodeEdgeIDList(b.Get(key))
	for _, eid := range existing {
		if eid == id {
			return
		}
	}
	existing = append(existing, id)
	b.Put(key, encodeEdgeIDList(existing))
}

func removeFromEdgeIDList(b *bolt.Bucket, key []byte, id string) {
	existing := decodeEdgeIDList(b.Get(key))
	for i, eid := range existing {
		if eid == id {
			existing = append(existing[:i], existing[i+1:]...)
			if len(existing) == 0 {
				b.Delete(key)
			} else {
				b.Put(key, encodeEdgeIDList(existing))
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

func (s *BboltEdgeStore) Clear() {
	s.cache = newEdgeLRU(s.cache.capacity)
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
