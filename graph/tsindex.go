package graph

import (
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	commitTimestampsBucket     = []byte("commit_timestamps")
	commitTimestampsMetaBucket = []byte("commit_timestamps_meta")
)

// TSIndex is a bbolt-backed index mapping commit timestamps to commit
// hashes (D7). Used by temporal queries that need to find commits by
// wall-clock time instead of walking the parent chain from HEAD.
//
// Bucket layout:
//
//	commit_timestamps      -> tsKey(timestamp, hash[:12]) -> full commit hash
//	commit_timestamps_meta -> reserved for future sentinels (unused in Phase 1)
//
// The key encodes time as 8-byte big-endian unix nanoseconds + '#' +
// up-to-12 char hash prefix. Big-endian byte order guarantees that
// lexicographic key order equals chronological order, so bbolt cursor
// Seek gives O(log N) snap-to-prior lookups and range scans. RFC3339Nano
// string keys were considered and rejected because Go drops trailing
// zero fractional digits, which breaks the lexicographic==chronological
// invariant.
//
// Concurrency: Put opens its own bbolt Update tx; PutTx accepts the
// caller's tx. CommitAt / CommitsBetween always open their own View;
// no tx argument.
type TSIndex struct {
	db *bolt.DB
}

// NewTSIndex opens or creates the timestamp index buckets.
func NewTSIndex(db *bolt.DB) (*TSIndex, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{
			commitTimestampsBucket,
			commitTimestampsMetaBucket,
		} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt tsindex: create buckets: %w", err)
	}
	return &TSIndex{db: db}, nil
}

// tsKey encodes (timestamp, short-hash) as a sortable bucket key.
// 8-byte big-endian unix nanos + '#' + up-to-12 char hash prefix.
func tsKey(t time.Time, hash string) []byte {
	short := hash
	if len(short) > 12 {
		short = short[:12]
	}
	key := make([]byte, 8+1+len(short))
	binary.BigEndian.PutUint64(key[:8], uint64(t.UTC().UnixNano()))
	key[8] = '#'
	copy(key[9:], short)
	return key
}

// Put writes a commit's index entry via its own tx. Idempotent: a
// repeat put for the same commit overwrites with identical value.
func (idx *TSIndex) Put(c *Commit) error {
	return idx.db.Update(func(tx *bolt.Tx) error {
		idx.PutTx(tx, c)
		return nil
	})
}

// PutTx writes a commit's index entry via the caller's tx.
func (idx *TSIndex) PutTx(tx *bolt.Tx, c *Commit) {
	b := tx.Bucket(commitTimestampsBucket)
	b.Put(tsKey(c.Timestamp, c.Hash), []byte(c.Hash))
}

// CommitAt returns the full hash of the commit at or strictly before
// t (snap-to-prior semantics, following Fluree's as-of-date contract).
// Returns ("", false) when the bucket is empty or t is before the
// earliest indexed commit.
func (idx *TSIndex) CommitAt(t time.Time) (string, bool) {
	targetNs := t.UTC().UnixNano()
	// Seek to the first key whose ns > target. If none, Last() is the
	// answer (or bucket is empty). Otherwise step Prev to land on the
	// last key with ns <= target.
	upper := make([]byte, 8)
	binary.BigEndian.PutUint64(upper, uint64(targetNs)+1)

	var hash string
	_ = idx.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commitTimestampsBucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		k, _ := c.Seek(upper)
		var v []byte
		if k == nil {
			k, v = c.Last()
		} else {
			k, v = c.Prev()
		}
		if k == nil {
			return nil
		}
		hash = string(v)
		return nil
	})
	return hash, hash != ""
}

// CommitsBetween returns the full hashes of commits with timestamps
// in [start, end] (inclusive both ends), in chronological order.
// Returns nil for an empty range (end < start) or no matches.
func (idx *TSIndex) CommitsBetween(start, end time.Time) []string {
	if end.Before(start) {
		return nil
	}
	startNs := start.UTC().UnixNano()
	endNs := end.UTC().UnixNano()

	lo := make([]byte, 8)
	binary.BigEndian.PutUint64(lo, uint64(startNs))

	var hashes []string
	_ = idx.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commitTimestampsBucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(lo); k != nil; k, v = c.Next() {
			if len(k) < 8 {
				continue
			}
			keyNs := int64(binary.BigEndian.Uint64(k[:8]))
			if keyNs > endNs {
				break
			}
			hashes = append(hashes, string(v))
		}
		return nil
	})
	return hashes
}

// Count returns the number of entries in the timestamp index.
// Useful for diagnostics and the `gramaton migrate` progress log.
func (idx *TSIndex) Count() int {
	var n int
	_ = idx.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commitTimestampsBucket)
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n
}
