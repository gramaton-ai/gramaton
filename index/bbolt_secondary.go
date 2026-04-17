package index

import (
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BboltSecondaryIndex maintains secondary indexes in bbolt (D16).
// These are lightweight acceleration structures that enable common
// queries without scanning the prolly tree:
//
//   - Time-sorted indexes (created_at, last_accessed) for recency queries
//   - Edge count cache for orphan detection
//   - Field existence bitmaps for missing-field queries
//
// All indexes are derived data, rebuildable from the graph.
//
// Concurrency: NOT thread-safe. The batch *bolt.Tx slot mutates
// without internal locking; this type relies on the engine's
// RWMutex serialising every caller. (Wave 7 P1-34.)
type BboltSecondaryIndex struct {
	db    *bolt.DB
	batch *bolt.Tx
}

var (
	timeCreatedBucket  = []byte("time:created_at")
	timeAccessedBucket = []byte("time:last_accessed")
	edgeCountBucket    = []byte("edge_counts")
	existsPrefix       = "exists:" // + field name
)

// NewBboltSecondaryIndex opens or creates secondary indexes.
func NewBboltSecondaryIndex(db *bolt.DB) (*BboltSecondaryIndex, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{timeCreatedBucket, timeAccessedBucket, edgeCountBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt secondary: create buckets: %w", err)
	}
	return &BboltSecondaryIndex{db: db}, nil
}

// SetBatch sets an external bbolt transaction for batching.
func (idx *BboltSecondaryIndex) SetBatch(tx *bolt.Tx) { idx.batch = tx }

// ClearBatch clears the external batch transaction.
func (idx *BboltSecondaryIndex) ClearBatch() { idx.batch = nil }

func (idx *BboltSecondaryIndex) update(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.Update(fn)
}

func (idx *BboltSecondaryIndex) view(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.View(fn)
}

// --- Time indexes ---

// timeKey encodes a timestamp + nodeID as a sortable key.
// Format: 8-byte big-endian unix nanos + nodeID.
// Big-endian ensures lexicographic sort = chronological sort.
func timeKey(t time.Time, nodeID string) []byte {
	key := make([]byte, 8+len(nodeID))
	binary.BigEndian.PutUint64(key[:8], uint64(t.UnixNano()))
	copy(key[8:], nodeID)
	return key
}

func parseTimeKey(key []byte) (time.Time, string) {
	if len(key) < 8 {
		return time.Time{}, ""
	}
	nanos := int64(binary.BigEndian.Uint64(key[:8]))
	return time.Unix(0, nanos), string(key[8:])
}

// SetCreatedAt records or updates a node's created_at timestamp.
func (idx *BboltSecondaryIndex) SetCreatedAt(nodeID string, t time.Time) {
	idx.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(timeCreatedBucket)
		// Remove old entry if exists (via nodeID lookup in value).
		idx.removeTimeEntry(b, nodeID)
		return b.Put(timeKey(t, nodeID), []byte(nodeID))
	})
}

// SetLastAccessed records or updates a node's last_accessed timestamp.
func (idx *BboltSecondaryIndex) SetLastAccessed(nodeID string, t time.Time) {
	idx.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(timeAccessedBucket)
		idx.removeTimeEntry(b, nodeID)
		return b.Put(timeKey(t, nodeID), []byte(nodeID))
	})
}

// removeTimeEntry removes all entries for a nodeID from a time bucket.
// Scans the bucket since nodeID appears in the value, not as the sole key.
func (idx *BboltSecondaryIndex) removeTimeEntry(b *bolt.Bucket, nodeID string) {
	c := b.Cursor()
	var toDelete [][]byte
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if string(v) == nodeID {
			toDelete = append(toDelete, append([]byte{}, k...))
		}
	}
	for _, k := range toDelete {
		b.Delete(k)
	}
}

// RecentByCreatedAt returns the N most recently created node IDs.
func (idx *BboltSecondaryIndex) RecentByCreatedAt(n int) []string {
	var ids []string
	idx.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(timeCreatedBucket)
		c := b.Cursor()
		// Reverse iteration (newest first).
		for k, _ := c.Last(); k != nil && len(ids) < n; k, _ = c.Prev() {
			_, nodeID := parseTimeKey(k)
			if nodeID != "" {
				ids = append(ids, nodeID)
			}
		}
		return nil
	})
	return ids
}

// RecentByLastAccessed returns the N most recently accessed node IDs.
func (idx *BboltSecondaryIndex) RecentByLastAccessed(n int) []string {
	var ids []string
	idx.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(timeAccessedBucket)
		c := b.Cursor()
		for k, _ := c.Last(); k != nil && len(ids) < n; k, _ = c.Prev() {
			_, nodeID := parseTimeKey(k)
			if nodeID != "" {
				ids = append(ids, nodeID)
			}
		}
		return nil
	})
	return ids
}

// --- Edge count cache ---

// SetEdgeCounts stores the in/out edge counts for a node.
func (idx *BboltSecondaryIndex) SetEdgeCounts(nodeID string, inCount, outCount int) {
	idx.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(edgeCountBucket)
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[:4], uint32(inCount))
		binary.LittleEndian.PutUint32(buf[4:], uint32(outCount))
		return b.Put([]byte(nodeID), buf[:])
	})
}

// GetEdgeCounts returns the cached in/out edge counts for a node.
func (idx *BboltSecondaryIndex) GetEdgeCounts(nodeID string) (in, out int, ok bool) {
	idx.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(edgeCountBucket)
		v := b.Get([]byte(nodeID))
		if len(v) >= 8 {
			in = int(binary.LittleEndian.Uint32(v[:4]))
			out = int(binary.LittleEndian.Uint32(v[4:]))
			ok = true
		}
		return nil
	})
	return
}

// Orphans returns node IDs with zero in+out edge count.
func (idx *BboltSecondaryIndex) Orphans() []string {
	var ids []string
	idx.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(edgeCountBucket)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if len(v) >= 8 {
				in := binary.LittleEndian.Uint32(v[:4])
				out := binary.LittleEndian.Uint32(v[4:])
				if in+out == 0 {
					ids = append(ids, string(k))
				}
			}
		}
		return nil
	})
	return ids
}

// RemoveNode removes a node from all secondary indexes.
func (idx *BboltSecondaryIndex) RemoveNode(nodeID string) {
	idx.update(func(tx *bolt.Tx) error {
		// Remove from time indexes.
		idx.removeTimeEntry(tx.Bucket(timeCreatedBucket), nodeID)
		idx.removeTimeEntry(tx.Bucket(timeAccessedBucket), nodeID)
		// Remove from edge counts.
		tx.Bucket(edgeCountBucket).Delete([]byte(nodeID))
		return nil
	})
}

// --- Field existence ---

// SetFieldExists marks that a node has a given field.
func (idx *BboltSecondaryIndex) SetFieldExists(field, nodeID string) {
	idx.update(func(tx *bolt.Tx) error {
		name := []byte(existsPrefix + field)
		b, err := tx.CreateBucketIfNotExists(name)
		if err != nil {
			return err
		}
		return b.Put([]byte(nodeID), []byte{1})
	})
}

// ClearFieldExists removes a node from a field existence index.
func (idx *BboltSecondaryIndex) ClearFieldExists(field, nodeID string) {
	idx.update(func(tx *bolt.Tx) error {
		name := []byte(existsPrefix + field)
		b := tx.Bucket(name)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(nodeID))
	})
}

// NodesWithField returns all node IDs that have the given field set.
func (idx *BboltSecondaryIndex) NodesWithField(field string) []string {
	var ids []string
	idx.view(func(tx *bolt.Tx) error {
		name := []byte(existsPrefix + field)
		b := tx.Bucket(name)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			ids = append(ids, string(k))
		}
		return nil
	})
	return ids
}

// NodesMissingField returns node IDs that are in allIDs but NOT in
// the field existence index. This enables missing=["temporality"]
// queries without scanning the prolly tree.
func (idx *BboltSecondaryIndex) NodesMissingField(field string, allIDs []string) []string {
	has := make(map[string]struct{})
	for _, id := range idx.NodesWithField(field) {
		has[id] = struct{}{}
	}
	var missing []string
	for _, id := range allIDs {
		if _, ok := has[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}
