package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
	bolt "go.etcd.io/bbolt"
)

// BboltPropertyIndex is a disk-backed PropertyIndex using bbolt (D2).
//
// Bucket layout:
//
//	exact:<key>   -> serialized_value -> encoded node ID set
//	kw:<key>      -> keyword_string   -> encoded node ID set
//	str:<key>     -> node_id          -> string value (for substring search)
//	nodekeys      -> node_id          -> encoded set of indexed keys
//
// Node ID sets are stored as sorted, length-prefixed strings:
//
//	uint32(count) + for each: uint16(len) + []byte(id)
//
// Range queries use bbolt cursor scan over exact:<key> bucket
// (keys are serialized Property values which sort correctly for
// same-type comparisons).
type BboltPropertyIndex struct {
	db    *bolt.DB
	batch *bolt.Tx // non-nil during Batch() call

	// indexedFields controls selective property indexing (D6).
	// If non-nil, only these fields (plus meta.* prefixed keys) are
	// indexed. If nil, all fields are indexed (backward compat).
	indexedFields map[string]struct{}
}

// DefaultIndexedFields is the set of fields indexed by default (D6).
// All meta.* keys are also indexed regardless of this list.
// content_full and content_short must be included because the search
// "match" feature uses the substring index (str: bucket) on them.
var DefaultIndexedFields = []string{
	"content_full",
	"content_short",
	"temporality",
	"knowledge_type",
	"epistemic_status",
	"resolution",
	"processing_status",
	"synthesis_status",
	"content_keywords",
}

// NewBboltPropertyIndex opens or creates a bbolt-backed property index.
// Only the fields in indexedFields (plus meta.* keys) are indexed.
// Pass nil for indexedFields to use DefaultIndexedFields.
func NewBboltPropertyIndex(db *bolt.DB, indexedFields []string) (*BboltPropertyIndex, error) {
	// Pre-create the nodekeys bucket.
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("nodekeys"))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt property index: create nodekeys bucket: %w", err)
	}
	var fm map[string]struct{}
	if indexedFields != nil {
		fm = make(map[string]struct{}, len(indexedFields))
		for _, f := range indexedFields {
			fm[f] = struct{}{}
		}
	}
	// nil indexedFields = index everything (for tests).
	// Non-nil = selective indexing (production).
	return &BboltPropertyIndex{db: db, indexedFields: fm}, nil
}

// Batch executes fn within a single bbolt write transaction. All Add
// and Remove calls within fn share one transaction, amortizing fsync.
// Critical for bulk operations (import, rebuild).
func (idx *BboltPropertyIndex) Batch(fn func()) error {
	if idx.batch != nil {
		// Already in a batch (e.g., external transaction via SetBatch).
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

// SetBatch sets an external bbolt transaction for batching. This allows
// the engine to open one transaction shared across multiple indexes
// (property + BM25) on the same bbolt DB.
func (idx *BboltPropertyIndex) SetBatch(tx *bolt.Tx) { idx.batch = tx }

// ClearBatch clears the external batch transaction.
func (idx *BboltPropertyIndex) ClearBatch() { idx.batch = nil }

// update runs fn in the current batch transaction or a new one.
func (idx *BboltPropertyIndex) update(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.Update(fn)
}

// view runs fn in the current batch transaction (read) or a new one.
func (idx *BboltPropertyIndex) view(fn func(tx *bolt.Tx) error) error {
	if idx.batch != nil {
		return fn(idx.batch)
	}
	return idx.db.View(fn)
}

// isIndexed returns true if the key should be indexed.
func (idx *BboltPropertyIndex) isIndexed(key string) bool {
	if idx.indexedFields == nil {
		return true // no filter, index everything
	}
	if _, ok := idx.indexedFields[key]; ok {
		return true
	}
	// Always index meta.* keys.
	return len(key) > 5 && key[:5] == "meta."
}

func exactBucket(key string) []byte { return []byte("exact:" + key) }
func kwBucket(key string) []byte    { return []byte("kw:" + key) }
func strBucket(key string) []byte   { return []byte("str:" + key) }

func (idx *BboltPropertyIndex) Add(nodeID, key string, val graph.Property) {
	if !idx.isIndexed(key) {
		return
	}
	if err := idx.update(func(tx *bolt.Tx) error {
		// Track which keys this node has indexed.
		nk, err := tx.CreateBucketIfNotExists([]byte("nodekeys"))
		if err != nil {
			return err
		}
		addToIDSet(nk, []byte(nodeID), key)

		// Exact match index.
		eb, err := tx.CreateBucketIfNotExists(exactBucket(key))
		if err != nil {
			return err
		}
		serialized := serializeValue(val)
		addToIDSet(eb, []byte(serialized), nodeID)

		// Substring index (string type only).
		if val.Type == graph.TypeString {
			sb, err := tx.CreateBucketIfNotExists(strBucket(key))
			if err != nil {
				return err
			}
			sb.Put([]byte(nodeID), []byte(val.String()))
		}

		// Keyword index (string list type only).
		if val.Type == graph.TypeStringList {
			kb, err := tx.CreateBucketIfNotExists(kwBucket(key))
			if err != nil {
				return err
			}
			for _, kw := range val.StringList() {
				addToIDSet(kb, []byte(kw), nodeID)
			}
		}

		return nil
	}); err != nil {
		slog.Error("bbolt property index: add", "node", nodeID, "key", key, "err", err)
	}
}

func (idx *BboltPropertyIndex) Remove(nodeID, key string, val graph.Property) {
	if !idx.isIndexed(key) {
		return
	}
	if err := idx.update(func(tx *bolt.Tx) error {
		// Exact match.
		if eb := tx.Bucket(exactBucket(key)); eb != nil {
			serialized := serializeValue(val)
			removeFromIDSet(eb, []byte(serialized), nodeID)
		}

		// Substring index.
		if val.Type == graph.TypeString {
			if sb := tx.Bucket(strBucket(key)); sb != nil {
				sb.Delete([]byte(nodeID))
			}
		}

		// Keyword index.
		if val.Type == graph.TypeStringList {
			if kb := tx.Bucket(kwBucket(key)); kb != nil {
				for _, kw := range val.StringList() {
					removeFromIDSet(kb, []byte(kw), nodeID)
				}
			}
		}

		// Update nodekeys.
		if nk := tx.Bucket([]byte("nodekeys")); nk != nil {
			removeFromIDSet(nk, []byte(nodeID), key)
		}

		return nil
	}); err != nil {
		slog.Error("bbolt property index: remove", "node", nodeID, "key", key, "err", err)
	}
}

func (idx *BboltPropertyIndex) RemoveNode(nodeID string, props graph.Properties) {
	for key, val := range props {
		idx.Remove(nodeID, key, val)
	}
}

func (idx *BboltPropertyIndex) Lookup(key string, val graph.Property) []string {
	var result []string
	idx.view(func(tx *bolt.Tx) error {
		eb := tx.Bucket(exactBucket(key))
		if eb == nil {
			return nil
		}
		serialized := serializeValue(val)
		result = decodeIDSet(eb.Get([]byte(serialized)))
		return nil
	})
	return result
}

func (idx *BboltPropertyIndex) Range(key string, min, max graph.Property) []string {
	var result []string
	idx.view(func(tx *bolt.Tx) error {
		eb := tx.Bucket(exactBucket(key))
		if eb == nil {
			return nil
		}
		// Scan all entries and filter by range. The bucket keys are
		// serialized Property values. We deserialize and compare.
		seen := make(map[string]struct{})
		c := eb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			prop, err := deserializeValue(k)
			if err != nil {
				continue
			}
			if prop.Compare(min) >= 0 && prop.Compare(max) <= 0 {
				for _, id := range decodeIDSet(v) {
					if _, ok := seen[id]; !ok {
						seen[id] = struct{}{}
						result = append(result, id)
					}
				}
			}
		}
		return nil
	})
	return result
}

func (idx *BboltPropertyIndex) Contains(key, substring string) []string {
	var result []string
	idx.view(func(tx *bolt.Tx) error {
		sb := tx.Bucket(strBucket(key))
		if sb == nil {
			return nil
		}
		c := sb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if strings.Contains(string(v), substring) {
				result = append(result, string(k))
			}
		}
		return nil
	})
	return result
}

func (idx *BboltPropertyIndex) ContainsFold(key, substring string) []string {
	var result []string
	lowerSub := strings.ToLower(substring)
	idx.view(func(tx *bolt.Tx) error {
		sb := tx.Bucket(strBucket(key))
		if sb == nil {
			return nil
		}
		c := sb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if strings.Contains(strings.ToLower(string(v)), lowerSub) {
				result = append(result, string(k))
			}
		}
		return nil
	})
	return result
}

func (idx *BboltPropertyIndex) LookupKeyword(key, keyword string) []string {
	var result []string
	idx.view(func(tx *bolt.Tx) error {
		kb := tx.Bucket(kwBucket(key))
		if kb == nil {
			return nil
		}
		result = decodeIDSet(kb.Get([]byte(keyword)))
		return nil
	})
	return result
}

func (idx *BboltPropertyIndex) NodesWithKey(key string) map[string]struct{} {
	result := make(map[string]struct{})
	idx.view(func(tx *bolt.Tx) error {
		eb := tx.Bucket(exactBucket(key))
		if eb == nil {
			return nil
		}
		c := eb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			for _, id := range decodeIDSet(v) {
				result[id] = struct{}{}
			}
		}
		return nil
	})
	if len(result) == 0 {
		return nil
	}
	return result
}

func (idx *BboltPropertyIndex) KeywordCounts(key string) map[string]int {
	counts := make(map[string]int)
	idx.view(func(tx *bolt.Tx) error {
		kb := tx.Bucket(kwBucket(key))
		if kb == nil {
			return nil
		}
		c := kb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			ids := decodeIDSet(v)
			if len(ids) > 0 {
				counts[string(k)] = len(ids)
			}
		}
		return nil
	})
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func (idx *BboltPropertyIndex) Count() int {
	total := 0
	idx.view(func(tx *bolt.Tx) error {
		tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			if bytes.HasPrefix(name, []byte("exact:")) {
				c := b.Cursor()
				for k, v := c.First(); k != nil; k, v = c.Next() {
					total += countIDSet(v)
				}
			}
			return nil
		})
		return nil
	})
	return total
}

// --- ID set encoding ---
//
// A set of string IDs is encoded as:
//   uint32(count) + for each: uint16(len) + []byte(id)
//
// This is compact and fast to decode. Sorted for determinism.

func encodeIDSet(ids []string) []byte {
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

func decodeIDSet(data []byte) []string {
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

func countIDSet(data []byte) int {
	if len(data) < 4 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(data[:4]))
}

// addToIDSet adds an ID to the set stored at the given key in the bucket.
func addToIDSet(b *bolt.Bucket, key []byte, id string) {
	existing := decodeIDSet(b.Get(key))
	// Check for duplicate.
	for _, eid := range existing {
		if eid == id {
			return
		}
	}
	existing = append(existing, id)
	b.Put(key, encodeIDSet(existing))
}

// removeFromIDSet removes an ID from the set stored at the given key.
func removeFromIDSet(b *bolt.Bucket, key []byte, id string) {
	existing := decodeIDSet(b.Get(key))
	for i, eid := range existing {
		if eid == id {
			existing = append(existing[:i], existing[i+1:]...)
			if len(existing) == 0 {
				b.Delete(key)
			} else {
				b.Put(key, encodeIDSet(existing))
			}
			return
		}
	}
}

// deserializeValue reconstructs a Property from its binary form.
func deserializeValue(data []byte) (graph.Property, error) {
	return graph.UnmarshalProperty(data)
}
