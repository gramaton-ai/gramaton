package graph

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/storage"
)

// RecordFloor is one record's retention watermark: content older
// than this survives only as changelog metadata.
type RecordFloor struct {
	// KeptFromCommit is the oldest version commit whose content blob
	// still exists; versions before it were swept.
	KeptFromCommit string    `json:"kept_from_commit,omitempty"`
	KeptFromTS     time.Time `json:"kept_from_ts,omitzero"`
	SweptVersions  int       `json:"swept_versions,omitempty"`
}

// Tombstone is the retention record a prune run mints: the history
// floor plus per-record swept-depth watermarks, O(records) not
// O(blobs). It lives as a CAS chunk referenced by the prune commit's
// PruneTombstoneRoot, so it is substrate-proper: index rebuilds
// cannot lose it and backups carry it. Successive prunes union the
// previous tombstone into the new one, so the newest is always
// complete. Readers consult it FIRST when history is absent -- a
// missing blob covered by a watermark is "pruned by policy", never
// corruption.
type Tombstone struct {
	// FloorDate is the chain-truncation horizon: no commit metadata
	// exists before it. Zero when only content-depth pruning has run.
	FloorDate time.Time `json:"floor_date,omitzero"`
	// OldestKeptCommit is the first commit at/after the floor -- the
	// oldest point as_of can resolve.
	OldestKeptCommit string `json:"oldest_kept_commit,omitempty"`
	// Baseline references the synthetic parentless commit capturing
	// the pre-floor tree state, so diffs against the oldest kept
	// commit still ground out. Reachable only through this field.
	Baseline string `json:"baseline,omitempty"`
	// Records maps record id -> its content-depth watermark.
	Records map[string]RecordFloor `json:"records,omitempty"`
	// PrunedAt stamps the run (latest run when unioned).
	PrunedAt time.Time `json:"pruned_at,omitzero"`
}

// WriteChunk writes the tombstone as a CAS chunk and returns its
// hash. (Named to stay out of the commit-minting Save namespace the
// saveactions lint guards.)
func (t *Tombstone) WriteChunk(store *storage.Store) (string, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("tombstone: marshal: %w", err)
	}
	hash, err := store.Write(data)
	if err != nil {
		return "", fmt.Errorf("tombstone: write: %w", err)
	}
	return hash, nil
}

// LoadTombstone reads a tombstone chunk by hash.
func LoadTombstone(store *storage.Store, hash string) (*Tombstone, error) {
	data, err := store.Read(hash)
	if err != nil {
		return nil, err
	}
	var t Tombstone
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("tombstone: unmarshal: %w", err)
	}
	return &t, nil
}

// Union folds an older tombstone into this one: the newer floor and
// per-record watermarks win; records only the old prune touched are
// carried forward. Call on the NEW tombstone with the PREVIOUS one.
func (t *Tombstone) Union(prev *Tombstone) {
	if prev == nil {
		return
	}
	if t.FloorDate.IsZero() {
		t.FloorDate = prev.FloorDate
		t.OldestKeptCommit = prev.OldestKeptCommit
		t.Baseline = prev.Baseline
	}
	if t.Records == nil && len(prev.Records) > 0 {
		t.Records = make(map[string]RecordFloor, len(prev.Records))
	}
	for id, old := range prev.Records {
		cur, ok := t.Records[id]
		if !ok {
			t.Records[id] = old
			continue
		}
		cur.SweptVersions += old.SweptVersions
		t.Records[id] = cur
	}
}

// CoversRecordVersion reports whether the watermarks explain a
// missing content blob for the record's version at ts: true means
// "pruned by policy", false leaves the absence unexplained
// (corruption-class).
func (t *Tombstone) CoversRecordVersion(recordID string, ts time.Time) bool {
	if t == nil {
		return false
	}
	if !t.FloorDate.IsZero() && ts.Before(t.FloorDate) {
		return true
	}
	rf, ok := t.Records[recordID]
	if !ok {
		return false
	}
	return !rf.KeptFromTS.IsZero() && ts.Before(rf.KeptFromTS)
}
