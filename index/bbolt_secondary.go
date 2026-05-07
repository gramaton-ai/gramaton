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
// Concurrency: Mutating methods take an explicit *bolt.Tx; Non-Tx
// methods open their own db.Update. Removes the stashed-pointer
// race class.
type BboltSecondaryIndex struct {
	db *bolt.DB
}

var (
	timeCreatedBucket     = []byte("time:created_at")
	timeAccessedBucket    = []byte("time:last_accessed")
	timeCreatedRevBucket  = []byte("time:created_at:rev")
	timeAccessedRevBucket = []byte("time:last_accessed:rev")
	edgeCountBucket       = []byte("edge_counts")
	existsPrefix          = "exists:" // + field name
)

// NewBboltSecondaryIndex opens or creates secondary indexes.
func NewBboltSecondaryIndex(db *bolt.DB) (*BboltSecondaryIndex, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		buckets := [][]byte{
			timeCreatedBucket, timeAccessedBucket,
			timeCreatedRevBucket, timeAccessedRevBucket,
			edgeCountBucket,
		}
		for _, name := range buckets {
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

// SetCreatedAt records or updates a node's created_at timestamp via its own tx.
func (idx *BboltSecondaryIndex) SetCreatedAt(nodeID string, t time.Time) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.SetCreatedAtTx(tx, nodeID, t)
		return nil
	})
}

// SetCreatedAtTx records or updates a node's created_at timestamp via the caller's tx.
func (idx *BboltSecondaryIndex) SetCreatedAtTx(tx *bolt.Tx, nodeID string, t time.Time) {
	b := tx.Bucket(timeCreatedBucket)
	rev := tx.Bucket(timeCreatedRevBucket)
	idx.removeTimeEntry(b, rev, nodeID)
	key := timeKey(t, nodeID)
	b.Put(key, []byte(nodeID))
	if rev != nil {
		rev.Put([]byte(nodeID), key)
	}
}

// SetLastAccessed records or updates a node's last_accessed timestamp via its own tx.
func (idx *BboltSecondaryIndex) SetLastAccessed(nodeID string, t time.Time) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.SetLastAccessedTx(tx, nodeID, t)
		return nil
	})
}

// SetLastAccessedTx records or updates a node's last_accessed timestamp via the caller's tx.
func (idx *BboltSecondaryIndex) SetLastAccessedTx(tx *bolt.Tx, nodeID string, t time.Time) {
	b := tx.Bucket(timeAccessedBucket)
	rev := tx.Bucket(timeAccessedRevBucket)
	idx.removeTimeEntry(b, rev, nodeID)
	key := timeKey(t, nodeID)
	b.Put(key, []byte(nodeID))
	if rev != nil {
		rev.Put([]byte(nodeID), key)
	}
}

// removeTimeEntry removes a nodeID's entry from a time bucket. Uses the
// reverse index (nodeID -> timestamp-key) for O(log N) deletion. Falls
// back to a full-bucket scan if the reverse index is missing for this
// node -- handles legacy data from before the reverse-index migration
// landed. After one full update cycle, all nodes have reverse entries
// and the scan path is unreachable.
func (idx *BboltSecondaryIndex) removeTimeEntry(b, rev *bolt.Bucket, nodeID string) {
	if rev != nil {
		if oldKey := rev.Get([]byte(nodeID)); oldKey != nil {
			k := append([]byte{}, oldKey...) // bbolt slices are tx-scoped
			b.Delete(k)
			rev.Delete([]byte(nodeID))
			return
		}
	}
	// Legacy path: scan bucket to find entries whose value is nodeID.
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
	idx.db.View(func(tx *bolt.Tx) error {
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
	idx.db.View(func(tx *bolt.Tx) error {
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

// SetEdgeCounts stores the in/out edge counts for a node via its own tx.
func (idx *BboltSecondaryIndex) SetEdgeCounts(nodeID string, inCount, outCount int) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.SetEdgeCountsTx(tx, nodeID, inCount, outCount)
		return nil
	})
}

// SetEdgeCountsTx stores the in/out edge counts via the caller's tx.
func (idx *BboltSecondaryIndex) SetEdgeCountsTx(tx *bolt.Tx, nodeID string, inCount, outCount int) {
	b := tx.Bucket(edgeCountBucket)
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[:4], uint32(inCount))
	binary.LittleEndian.PutUint32(buf[4:], uint32(outCount))
	b.Put([]byte(nodeID), buf[:])
}

// GetEdgeCounts returns the cached in/out edge counts for a node.
func (idx *BboltSecondaryIndex) GetEdgeCounts(nodeID string) (in, out int, ok bool) {
	idx.db.View(func(tx *bolt.Tx) error {
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
	idx.db.View(func(tx *bolt.Tx) error {
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

// RemoveNode removes a node from all secondary indexes via its own tx.
func (idx *BboltSecondaryIndex) RemoveNode(nodeID string) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.RemoveNodeTx(tx, nodeID)
		return nil
	})
}

// RemoveNodeTx removes a node from all secondary indexes via the caller's tx.
func (idx *BboltSecondaryIndex) RemoveNodeTx(tx *bolt.Tx, nodeID string) {
	idx.removeTimeEntry(tx.Bucket(timeCreatedBucket), tx.Bucket(timeCreatedRevBucket), nodeID)
	idx.removeTimeEntry(tx.Bucket(timeAccessedBucket), tx.Bucket(timeAccessedRevBucket), nodeID)
	tx.Bucket(edgeCountBucket).Delete([]byte(nodeID))
}

// --- Field existence ---

// SetFieldExists marks that a node has a given field via its own tx.
func (idx *BboltSecondaryIndex) SetFieldExists(field, nodeID string) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.SetFieldExistsTx(tx, field, nodeID)
		return nil
	})
}

// SetFieldExistsTx marks field existence via the caller's tx.
func (idx *BboltSecondaryIndex) SetFieldExistsTx(tx *bolt.Tx, field, nodeID string) {
	name := []byte(existsPrefix + field)
	b, err := tx.CreateBucketIfNotExists(name)
	if err != nil {
		return
	}
	b.Put([]byte(nodeID), []byte{1})
}

// ClearFieldExists removes a node from a field existence index via its own tx.
func (idx *BboltSecondaryIndex) ClearFieldExists(field, nodeID string) {
	idx.db.Update(func(tx *bolt.Tx) error {
		idx.ClearFieldExistsTx(tx, field, nodeID)
		return nil
	})
}

// ClearFieldExistsTx clears field existence via the caller's tx.
func (idx *BboltSecondaryIndex) ClearFieldExistsTx(tx *bolt.Tx, field, nodeID string) {
	name := []byte(existsPrefix + field)
	b := tx.Bucket(name)
	if b == nil {
		return
	}
	b.Delete([]byte(nodeID))
}

// NodesWithField returns all node IDs that have the given field set.
func (idx *BboltSecondaryIndex) NodesWithField(field string) []string {
	var ids []string
	idx.db.View(func(tx *bolt.Tx) error {
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

