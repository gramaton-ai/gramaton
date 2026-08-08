package index

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// AccessMeta is one record's non-versioned operational bookkeeping:
// how often and how recently it was read, and how many embed attempts
// it has burned. These values used to live in versioned node
// properties, which meant every read dirtied the accessed node and a
// periodic flush committed the churn -- the mechanism behind
// million-commit histories on modest stores. They now live here,
// outside the commit substrate entirely.
type AccessMeta struct {
	Count         int64     `json:"count,omitempty"`
	LastAccessed  time.Time `json:"last_accessed,omitzero"`
	EmbedAttempts int64     `json:"embed_attempts,omitempty"`
}

// BboltAccessIndex is the access-metadata sidecar. It lives in its
// own bbolt file (sidecar.db), NOT indexes.db: indexes.db is derived
// state that backup excludes and restore rebuilds, while access
// metadata is primary (unrebuildable) bookkeeping that must survive a
// backup/restore cycle.
//
// A write-through in-memory map fronts the bucket so the node
// materialization hook (which runs per node on graph loads and
// iterator scans) costs a map lookup, never a bbolt read. The map is
// loaded once at open; entries are small (three fields per record).
type BboltAccessIndex struct {
	db *bolt.DB

	mu    sync.RWMutex
	cache map[string]AccessMeta
}

var accessBucket = []byte("access_meta")

// NewBboltAccessIndex opens the sidecar over db and loads the cache.
func NewBboltAccessIndex(db *bolt.DB) (*BboltAccessIndex, error) {
	idx := &BboltAccessIndex{db: db, cache: make(map[string]AccessMeta)}
	err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(accessBucket)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, v []byte) error {
			var m AccessMeta
			if err := json.Unmarshal(v, &m); err != nil {
				// A corrupt entry degrades to re-seeding from the
				// record's legacy blob values; never fail the open.
				return nil
			}
			idx.cache[string(k)] = m
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt access sidecar: open: %w", err)
	}
	return idx, nil
}

// Get returns the record's access metadata from the in-memory cache.
func (idx *BboltAccessIndex) Get(nodeID string) (AccessMeta, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	m, ok := idx.cache[nodeID]
	return m, ok
}

// Put stores the record's access metadata: cache first, then the
// bucket via its own transaction.
func (idx *BboltAccessIndex) Put(nodeID string, m AccessMeta) {
	idx.mu.Lock()
	idx.cache[nodeID] = m
	idx.mu.Unlock()

	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := idx.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(accessBucket).Put([]byte(nodeID), data)
	}); err != nil {
		// The cache keeps serving the value and the next Put retries
		// the persist. Access metadata is bookkeeping -- degrade,
		// don't fail the read path that triggered it.
		slog.Error("access sidecar: persist failed", "component", "index", "node", nodeID, "err", err)
	}
}

// Remove deletes the record's access metadata (node deletion).
func (idx *BboltAccessIndex) Remove(nodeID string) {
	idx.mu.Lock()
	delete(idx.cache, nodeID)
	idx.mu.Unlock()
	_ = idx.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(accessBucket).Delete([]byte(nodeID))
	})
}

// PutBatch stores many records' access metadata in one transaction:
// cache first, then a single bucket update. Search bumps every
// result under the engine write lock, and one fsynced transaction
// per result would stall all readers for the batch; this pays one.
func (idx *BboltAccessIndex) PutBatch(batch map[string]AccessMeta) {
	if len(batch) == 0 {
		return
	}
	idx.mu.Lock()
	for id, m := range batch {
		idx.cache[id] = m
	}
	idx.mu.Unlock()

	if err := idx.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(accessBucket)
		for id, m := range batch {
			data, err := json.Marshal(m)
			if err != nil {
				continue
			}
			if err := b.Put([]byte(id), data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		slog.Error("access sidecar: batch persist failed", "component", "index", "records", len(batch), "err", err)
	}
}
