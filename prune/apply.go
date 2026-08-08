package prune

import (
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// ApplyResult reports what an executed prune actually did.
type ApplyResult struct {
	Commit        *graph.Commit
	TombstoneRoot string
	SweptBlobs    int
	SweptBytes    int64
	SweepErrors   int
}

// ApplyKeepVersions executes a keep-versions plan. Order is
// deliberate insurance sequencing: the tombstone chunk and the prune
// commit referencing it land BEFORE the first blob is deleted, so a
// crash mid-sweep leaves every already-missing blob explained by an
// installed floor -- never a window where content is gone and the
// store has no record of why.
//
// The caller (the CLI confirm path) is responsible for verifying the
// plan is not stale (HEAD and refs unchanged since planning); this
// function trusts the plan.
func ApplyKeepVersions(eng *core.Engine, plan *KeepVersionsPlan) (*ApplyResult, error) {
	if plan == nil || plan.BlobCount == 0 {
		return nil, fmt.Errorf("nothing to sweep")
	}
	store := eng.Store()

	ts := &graph.Tombstone{
		Records:  make(map[string]graph.RecordFloor, len(plan.Records)),
		PrunedAt: time.Now().UTC(),
	}
	for _, rs := range plan.Records {
		ts.Records[rs.RecordID] = graph.RecordFloor{
			KeptFromCommit: rs.KeptFromCommit,
			KeptFromTS:     rs.KeptFromTS,
			SweptVersions:  len(rs.SweepHashes),
		}
	}
	ts.Union(eng.HistoryFloor())

	root, err := ts.WriteChunk(store)
	if err != nil {
		return nil, fmt.Errorf("tombstone: %w", err)
	}

	eng.Lock()
	eng.SetPendingTombstoneRoot(root)
	commit, err := eng.Save("prune: content depth sweep", graph.CommitAction{Kind: graph.ActionPrune})
	eng.Unlock()
	if err != nil {
		return nil, fmt.Errorf("prune commit: %w", err)
	}
	if err := eng.Changelog().SetPruneTombstoneRef(root); err != nil {
		return nil, fmt.Errorf("tombstone pointer: %w", err)
	}
	eng.SetHistoryFloor(ts)

	res := &ApplyResult{Commit: commit, TombstoneRoot: root}
	for _, rs := range plan.Records {
		for _, h := range rs.SweepHashes {
			sz, _ := store.Size(h)
			if err := store.Delete(h); err != nil {
				res.SweepErrors++
				continue
			}
			res.SweptBlobs++
			res.SweptBytes += sz
		}
	}
	return res, nil
}
