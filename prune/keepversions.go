// Package prune implements offline retention: per-record content
// depth (--keep-versions) and chain truncation (--older-than).
// Deliberately CLI-only -- no api/ operation exists and none may be
// added; destructive history removal is a human-operated command.
package prune

import (
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/storage"
)

// RecordSweep is one record's share of a keep-versions plan.
type RecordSweep struct {
	RecordID       string    `json:"record_id"`
	SweepHashes    []string  `json:"sweep_hashes"`
	KeptFromCommit string    `json:"kept_from_commit,omitempty"`
	KeptFromTS     time.Time `json:"kept_from_ts,omitzero"`
	RecordDeleted  bool      `json:"record_deleted,omitempty"`
	// NewestSweptTS drives the backup-coverage check: the newest
	// content being removed must exist in the verified backup.
	NewestSweptTS time.Time `json:"newest_swept_ts,omitzero"`
}

// KeepVersionsPlan lists exactly what a --keep-versions run would
// remove. Planning is read-only.
type KeepVersionsPlan struct {
	KeepVersions int           `json:"keep_versions"`
	Records      []RecordSweep `json:"records"`
	BlobCount    int           `json:"blob_count"`
	Bytes        int64         `json:"bytes"`
	// NewestSweptTS is the max across records (backup gate input).
	NewestSweptTS time.Time `json:"newest_swept_ts,omitzero"`
}

// PlanKeepVersions builds the per-record content-depth plan:
// changelog-keyed candidates (every logical version blob beyond the
// newest keep, all versions of since-deleted records) minus the mark
// set (the full node state of HEAD and every other ref tip, plus the
// kept version blobs). Commit chain and tree metadata are never
// candidates -- the timeline survives as metadata.
//
// Refuses until the changelog marker == HEAD: retention slots are
// counted in content-distinct logical versions, and an incomplete
// changelog would miscount them.
func PlanKeepVersions(eng *core.Engine, keep int, refTips map[string]string) (*KeepVersionsPlan, error) {
	if keep < 1 {
		return nil, fmt.Errorf("keep-versions must be >= 1")
	}
	cl := eng.Changelog()
	if cl == nil {
		return nil, fmt.Errorf("changelog index unavailable; cannot count versions")
	}
	if cl.Marker() != eng.HeadHash() {
		return nil, fmt.Errorf("changelog is behind HEAD; run 'gramaton backfill changelog' first")
	}
	store := eng.Store()

	// Mark: full node state at HEAD and at every other ref tip.
	mark := make(map[string]struct{})
	markState := func(commitHash string) error {
		commit, err := graph.LoadCommitMeta(store, commitHash)
		if err != nil {
			return fmt.Errorf("load ref tip %s: %w", core.TruncHash(commitHash), err)
		}
		if commit.NodeTreeRoot == "" {
			return nil
		}
		entries, err := storage.LoadProllyTree(store, commit.NodeTreeRoot).AllEntries()
		if err != nil {
			return fmt.Errorf("walk tree of %s: %w", core.TruncHash(commitHash), err)
		}
		for _, e := range entries {
			mark[e.Value] = struct{}{}
		}
		return nil
	}
	if err := markState(eng.HeadHash()); err != nil {
		return nil, err
	}
	for name, tip := range refTips {
		if tip == eng.HeadHash() {
			continue
		}
		if err := markState(tip); err != nil {
			return nil, fmt.Errorf("ref %q: %w", name, err)
		}
	}

	plan := &KeepVersionsPlan{KeepVersions: keep}
	g := eng.Graph()
	err := cl.ForEach(func(nodeID string, entries []index.ChangelogEntry) error {
		_, alive := g.GetNode(nodeID)
		// Content-bearing versions only (deletion entries carry none).
		var content []index.ChangelogEntry
		for _, e := range entries {
			if e.NodeHash != "" {
				content = append(content, e)
			}
		}
		cut := len(content) - keep
		if !alive {
			// Since-deleted records are fully sweepable; the collapse
			// archive or backup is the insurance.
			cut = len(content)
		}
		if cut <= 0 {
			return nil
		}
		// Kept versions join the mark set so shared CAS blobs are
		// never swept through another record's candidacy.
		for _, e := range content[cut:] {
			mark[e.NodeHash] = struct{}{}
		}
		rs := RecordSweep{RecordID: nodeID, RecordDeleted: !alive}
		if cut < len(content) {
			rs.KeptFromCommit = content[cut].Commit
			rs.KeptFromTS = content[cut].Timestamp
		}
		for _, e := range content[:cut] {
			if _, marked := mark[e.NodeHash]; marked {
				continue
			}
			if !store.Has(e.NodeHash) {
				continue // already swept by an earlier prune
			}
			rs.SweepHashes = append(rs.SweepHashes, e.NodeHash)
			if e.Timestamp.After(rs.NewestSweptTS) {
				rs.NewestSweptTS = e.Timestamp
			}
		}
		if len(rs.SweepHashes) == 0 {
			return nil
		}
		plan.Records = append(plan.Records, rs)
		plan.BlobCount += len(rs.SweepHashes)
		if rs.NewestSweptTS.After(plan.NewestSweptTS) {
			plan.NewestSweptTS = rs.NewestSweptTS
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Cross-record dedup: a CAS blob can be a candidate under two
	// records (identical content); count and delete it once.
	seen := make(map[string]struct{}, plan.BlobCount)
	total := 0
	for i := range plan.Records {
		kept := plan.Records[i].SweepHashes[:0]
		for _, h := range plan.Records[i].SweepHashes {
			if _, dup := seen[h]; dup {
				continue
			}
			seen[h] = struct{}{}
			kept = append(kept, h)
		}
		plan.Records[i].SweepHashes = kept
		total += len(kept)
	}
	plan.BlobCount = total
	for h := range seen {
		if sz, err := store.Size(h); err == nil {
			plan.Bytes += sz
		}
	}
	return plan, nil
}
