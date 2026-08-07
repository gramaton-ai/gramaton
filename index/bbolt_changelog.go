package index

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ChangelogEntry is one logical version of one record: the commit
// that minted it and the record's content hash there. An empty
// NodeHash marks the record's deletion at that commit. Author and
// change_note are NOT duplicated here -- they live on the commit,
// which timeline readers load by hash.
type ChangelogEntry struct {
	Commit    string    `json:"c"`
	NodeHash  string    `json:"h,omitempty"`
	Timestamp time.Time `json:"t"`
}

// BboltChangelog is the per-node changelog index in sidecar.db:
// nodeID -> append-only list of logical versions. Entries are
// LOGICAL versions per the content-based rule (bookkeeping-masked
// comparison of adjacent blobs), not raw hash changes.
//
// Durability contract: Append writes its entries and the
// last_indexed_head marker in ONE transaction, and the engine calls
// it only after the commit's HEAD write. At boot, marker != HEAD
// means the process died between those two steps; the gap walk
// re-derives the missing entries from the commit chain. Drift is
// detected and repaired, never accumulated.
type BboltChangelog struct {
	db *bolt.DB
}

var (
	changelogBucket     = []byte("changelog")
	changelogMetaBucket = []byte("changelog_meta")
	markerKey           = []byte("last_indexed_head")
)

// NewBboltChangelog opens the changelog over db.
func NewBboltChangelog(db *bolt.DB) (*BboltChangelog, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{changelogBucket, changelogMetaBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt changelog: open: %w", err)
	}
	return &BboltChangelog{db: db}, nil
}

// Append records one commit's logical versions (keyed by node ID)
// and advances the marker to that commit, all in one transaction.
// Empty entries with a non-empty marker just advance the marker (a
// commit that minted no logical versions still counts as indexed).
func (c *BboltChangelog) Append(entries map[string]ChangelogEntry, marker string) error {
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(changelogBucket)
		for nodeID, e := range entries {
			var list []ChangelogEntry
			if raw := b.Get([]byte(nodeID)); raw != nil {
				if err := json.Unmarshal(raw, &list); err != nil {
					list = nil
				}
			}
			// Idempotence for gap-walk replays: skip when this commit
			// is already the node's latest entry.
			if len(list) > 0 && list[len(list)-1].Commit == e.Commit {
				continue
			}
			list = append(list, e)
			data, err := json.Marshal(list)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(nodeID), data); err != nil {
				return err
			}
		}
		if marker != "" {
			return tx.Bucket(changelogMetaBucket).Put(markerKey, []byte(marker))
		}
		return nil
	})
}

// Versions returns a record's logical versions, oldest first. Nil
// when the record has no indexed versions (which can mean "predates
// changelog coverage", not "never changed" -- callers report
// coverage, never certainty).
func (c *BboltChangelog) Versions(nodeID string) []ChangelogEntry {
	var list []ChangelogEntry
	_ = c.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket(changelogBucket).Get([]byte(nodeID)); raw != nil {
			_ = json.Unmarshal(raw, &list)
		}
		return nil
	})
	return list
}

// Marker returns the last indexed HEAD, or "" when the changelog has
// never been initialized on this store.
func (c *BboltChangelog) Marker() string {
	var m string
	_ = c.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket(changelogMetaBucket).Get(markerKey); raw != nil {
			m = string(raw)
		}
		return nil
	})
	return m
}

// SetMarker overwrites the marker without touching entries. Used by
// coverage resets (a marker orphaned off the current chain) and the
// offline backfill.
func (c *BboltChangelog) SetMarker(h string) error {
	return c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(changelogMetaBucket).Put(markerKey, []byte(h))
	})
}

// RetractCommits removes every entry minted by the given commits
// (checkout/revert rebuild: entries exclusive to the abandoned
// lineage). Bounded by the divergence, not the chain.
func (c *BboltChangelog) RetractCommits(commits map[string]bool) error {
	if len(commits) == 0 {
		return nil
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(changelogBucket)
		cur := b.Cursor()
		type rewrite struct {
			key  []byte
			data []byte
			del  bool
		}
		var rewrites []rewrite
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var list []ChangelogEntry
			if err := json.Unmarshal(v, &list); err != nil {
				continue
			}
			kept := list[:0]
			for _, e := range list {
				if !commits[e.Commit] {
					kept = append(kept, e)
				}
			}
			if len(kept) == len(list) {
				continue
			}
			if len(kept) == 0 {
				rewrites = append(rewrites, rewrite{key: append([]byte(nil), k...), del: true})
				continue
			}
			data, err := json.Marshal(kept)
			if err != nil {
				return err
			}
			rewrites = append(rewrites, rewrite{key: append([]byte(nil), k...), data: data})
		}
		for _, rw := range rewrites {
			if rw.del {
				if err := b.Delete(rw.key); err != nil {
					return err
				}
				continue
			}
			if err := b.Put(rw.key, rw.data); err != nil {
				return err
			}
		}
		return nil
	})
}
