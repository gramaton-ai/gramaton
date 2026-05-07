package index

import (
	"encoding/binary"
	"log/slog"

	bolt "go.etcd.io/bbolt"
)

// BboltCollectionCache caches collection membership in bbolt (D24).
// Each collection's member IDs are stored as a single key-value pair,
// enabling O(1) lookups instead of scanning all inbound edges.
//
// Bucket layout:
//
//	collmembers -> collectionID -> encoded list of item node IDs
//
// Concurrency: Mutating methods take an explicit *bolt.Tx so the
// transaction is threaded via the call graph. The bulk-add deadlock
// gotcha (AddMember opening its own bbolt tx inside BatchIndexWrites)
// is now a compile-time concern: callers that hold a tx MUST use
// AddMemberTx, not AddMember. Removes the stashed-pointer
// race class.
type BboltCollectionCache struct {
	db *bolt.DB
}

var collMembersBucket = []byte("collmembers")

// NewBboltCollectionCache creates or opens the collection cache.
func NewBboltCollectionCache(db *bolt.DB) (*BboltCollectionCache, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(collMembersBucket)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &BboltCollectionCache{db: db}, nil
}

// AddMember adds an item ID to a collection's cached member list via its own tx.
func (c *BboltCollectionCache) AddMember(collectionID, itemID string) {
	if err := c.db.Update(func(tx *bolt.Tx) error {
		c.AddMemberTx(tx, collectionID, itemID)
		return nil
	}); err != nil {
		slog.Error("collection cache: add member failed", "collection", collectionID, "item", itemID, "err", err)
	}
}

// AddMemberTx adds an item ID via the caller's tx.
func (c *BboltCollectionCache) AddMemberTx(tx *bolt.Tx, collectionID, itemID string) {
	b := tx.Bucket(collMembersBucket)
	ids := decodeIDList(b.Get([]byte(collectionID)))
	for _, id := range ids {
		if id == itemID {
			return
		}
	}
	ids = append(ids, itemID)
	b.Put([]byte(collectionID), encodeIDList(ids))
}

// RemoveMember removes an item ID from a collection's cached member list via its own tx.
func (c *BboltCollectionCache) RemoveMember(collectionID, itemID string) {
	if err := c.db.Update(func(tx *bolt.Tx) error {
		c.RemoveMemberTx(tx, collectionID, itemID)
		return nil
	}); err != nil {
		slog.Error("collection cache: remove member failed", "collection", collectionID, "item", itemID, "err", err)
	}
}

// RemoveMemberTx removes an item ID via the caller's tx.
func (c *BboltCollectionCache) RemoveMemberTx(tx *bolt.Tx, collectionID, itemID string) {
	b := tx.Bucket(collMembersBucket)
	ids := decodeIDList(b.Get([]byte(collectionID)))
	for i, id := range ids {
		if id == itemID {
			ids = append(ids[:i], ids[i+1:]...)
			if len(ids) == 0 {
				b.Delete([]byte(collectionID))
			} else {
				b.Put([]byte(collectionID), encodeIDList(ids))
			}
			return
		}
	}
}

// Members returns all cached item IDs for a collection.
// Returns nil if the collection has no cached members.
func (c *BboltCollectionCache) Members(collectionID string) []string {
	var ids []string
	c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(collMembersBucket)
		ids = decodeIDList(b.Get([]byte(collectionID)))
		return nil
	})
	return ids
}

// MemberCount returns the cached member count for a collection.
func (c *BboltCollectionCache) MemberCount(collectionID string) int {
	count := 0
	c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(collMembersBucket)
		data := b.Get([]byte(collectionID))
		if len(data) >= 4 {
			count = int(binary.LittleEndian.Uint32(data[:4]))
		}
		return nil
	})
	return count
}

// DeleteCollection removes the cache entry for a collection via its own tx.
func (c *BboltCollectionCache) DeleteCollection(collectionID string) {
	if err := c.db.Update(func(tx *bolt.Tx) error {
		c.DeleteCollectionTx(tx, collectionID)
		return nil
	}); err != nil {
		slog.Error("collection cache: delete collection failed", "collection", collectionID, "err", err)
	}
}

// DeleteCollectionTx removes the cache entry via the caller's tx.
func (c *BboltCollectionCache) DeleteCollectionTx(tx *bolt.Tx, collectionID string) {
	tx.Bucket(collMembersBucket).Delete([]byte(collectionID))
}

// --- Encoding: uint32(count) + for each: uint16(len) + []byte(id) ---

func encodeIDList(ids []string) []byte {
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

func decodeIDList(data []byte) []string {
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
