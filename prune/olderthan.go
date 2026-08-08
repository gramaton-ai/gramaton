package prune

import (
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/storage"
)

// MinKeepCommits is the chain-truncation floor: a prune never leaves
// fewer commits than this regardless of horizon.
const MinKeepCommits = 5

// OlderThanPlan describes a chain truncation. Planning is read-only.
type OlderThanPlan struct {
	Horizon time.Time `json:"horizon"`
	// OldestKept is the commit the chain will ground out at (K).
	OldestKept   string    `json:"oldest_kept"`
	OldestKeptTS time.Time `json:"oldest_kept_ts"`
	// BaselineSource is K's parent, whose tree state the synthetic
	// baseline captures at apply time. Empty when K is the root.
	BaselineSource string `json:"baseline_source,omitempty"`
	TruncateCount  int    `json:"truncate_count"`
	ChainLength    int    `json:"chain_length"`
}

// PlanOlderThan resolves the horizon to an exact cut point: K is the
// oldest commit kept, chosen so every kept commit is at/after the
// horizon AND at least MinKeepCommits commits survive. Refuses when
// any ref's tip sits inside the truncated region -- a stale branch
// tip's tree must never dangle.
func PlanOlderThan(eng *core.Engine, horizon time.Time, refTips map[string]string) (*OlderThanPlan, error) {
	store := eng.Store()
	head := eng.HeadHash()
	if head == "" {
		return nil, fmt.Errorf("empty store")
	}

	// Walk HEAD back to root collecting (hash, ts, parent).
	type link struct {
		hash   string
		ts     time.Time
		parent string
	}
	var chain []link
	for cur := head; cur != ""; {
		c, err := graph.LoadCommitMeta(store, cur)
		if err != nil {
			return nil, fmt.Errorf("chain walk at %s: %w", core.TruncHash(cur), err)
		}
		chain = append(chain, link{hash: cur, ts: c.Timestamp, parent: c.Parent})
		cur = c.Parent
	}

	// chain[0] is HEAD. Find K: the deepest index i such that
	// chain[i].ts >= horizon, extended to at least MinKeepCommits.
	cut := 0
	for i, l := range chain {
		if l.ts.UTC().Before(horizon) {
			break
		}
		cut = i
	}
	if cut < MinKeepCommits-1 {
		cut = MinKeepCommits - 1
	}
	if cut >= len(chain)-1 {
		return nil, fmt.Errorf("nothing to truncate: the horizon (with the %d-commit floor) keeps the whole chain", MinKeepCommits)
	}
	k := chain[cut]

	for name, tip := range refTips {
		c, err := graph.LoadCommitMeta(store, tip)
		if err != nil {
			return nil, fmt.Errorf("ref %q tip unreadable: %w", name, err)
		}
		if c.Timestamp.UTC().Before(k.ts.UTC()) {
			return nil, fmt.Errorf("ref %q points into the truncated region; merge or discard that branch first", name)
		}
	}

	return &OlderThanPlan{
		Horizon:        horizon,
		OldestKept:     k.hash,
		OldestKeptTS:   k.ts,
		BaselineSource: k.parent,
		TruncateCount:  len(chain) - 1 - cut,
		ChainLength:    len(chain),
	}, nil
}

// ApplyOlderThan executes a chain truncation with the same
// insurance-first sequencing as the content-depth sweep: baseline
// and tombstone land in the CAS and the prune commit references them
// BEFORE the reachability sweep deletes anything; the derived
// indexes truncate last.
func ApplyOlderThan(eng *core.Engine, plan *OlderThanPlan, refTips map[string]string) (*ApplyResult, error) {
	if plan == nil || plan.TruncateCount == 0 {
		return nil, fmt.Errorf("nothing to truncate")
	}
	store := eng.Store()

	// Synthetic baseline: K's parent's tree state as a parentless
	// commit, so diffs against K still ground out after K's real
	// parent chunk is swept.
	baseline := ""
	if plan.BaselineSource != "" {
		src, err := graph.LoadCommitMeta(store, plan.BaselineSource)
		if err != nil {
			return nil, fmt.Errorf("baseline source: %w", err)
		}
		b := &graph.Commit{
			Version:      src.Version,
			Timestamp:    src.Timestamp,
			Message:      "prune baseline (pre-horizon state)",
			NodeTreeRoot: src.NodeTreeRoot,
			EdgeTreeRoot: src.EdgeTreeRoot,
			Author:       "prune",
		}
		baseline, err = graph.WriteCommitChunk(store, b)
		if err != nil {
			return nil, fmt.Errorf("baseline write: %w", err)
		}
	}

	kts := plan.OldestKeptTS.UTC()
	ts := &graph.Tombstone{
		FloorDate:        kts,
		OldestKeptCommit: plan.OldestKept,
		Baseline:         baseline,
		PrunedAt:         time.Now().UTC(),
	}
	ts.Union(eng.HistoryFloor())
	// A newer truncation always wins the floor fields.
	ts.FloorDate = kts
	ts.OldestKeptCommit = plan.OldestKept
	ts.Baseline = baseline

	root, err := ts.Save(store)
	if err != nil {
		return nil, fmt.Errorf("tombstone: %w", err)
	}

	eng.Lock()
	eng.SetPendingTombstoneRoot(root)
	commit, err := eng.Save("prune: chain truncation", graph.CommitAction{Kind: graph.ActionPrune})
	eng.Unlock()
	if err != nil {
		return nil, fmt.Errorf("prune commit: %w", err)
	}
	if err := eng.Changelog().SetPruneTombstoneRef(root); err != nil {
		return nil, fmt.Errorf("tombstone pointer: %w", err)
	}
	eng.SetHistoryFloor(ts)

	tips := make([]string, 0, len(refTips))
	for _, tip := range refTips {
		tips = append(tips, tip)
	}
	gcRes, err := store.GC(storage.GCOptions{
		Cutoff:         kts,
		MinKeepCommits: MinKeepCommits,
		HeadHash:       eng.HeadHash(),
		BranchTips:     func() []string { return tips },
		CommitLoader:   loadGCCommit(store),
		ExtraRoots:     []string{root},
		ExtraCommits:   []string{baseline},
	})
	if err != nil {
		return nil, fmt.Errorf("reachability sweep: %w", err)
	}

	res := &ApplyResult{Commit: commit, TombstoneRoot: root, SweptBlobs: gcRes.DeletedCount}
	if gcRes.Errors > 0 {
		res.SweepErrors = gcRes.Errors
	}

	// Derived-index truncation last: both are repairable (TSIndex by
	// rebuild, changelog by design -- entries for commits that no
	// longer exist are retracted, not lost knowledge).
	if _, err := eng.TSIndex().TruncateBefore(kts); err != nil {
		return res, fmt.Errorf("timestamp index truncation: %w", err)
	}
	if _, err := eng.Changelog().RetractBefore(kts); err != nil {
		return res, fmt.Errorf("changelog truncation: %w", err)
	}
	return res, nil
}

// loadGCCommit adapts the storage GC's commit loader to the CAS.
func loadGCCommit(store *storage.Store) func(string) (*storage.GCCommit, error) {
	return func(hash string) (*storage.GCCommit, error) {
		c, err := graph.LoadCommitMeta(store, hash)
		if err != nil {
			return nil, err
		}
		return &storage.GCCommit{
			Hash: c.Hash, Parent: c.Parent, Timestamp: c.Timestamp,
			NodeTreeRoot: c.NodeTreeRoot, EdgeTreeRoot: c.EdgeTreeRoot,
			PruneTombstoneRoot: c.PruneTombstoneRoot,
			BM25Root:           c.BM25Root, BM25FullRoot: c.BM25FullRoot,
			BM25MediumRoot: c.BM25MediumRoot, BM25ShortRoot: c.BM25ShortRoot,
			VecRoot: c.VecRoot, PropRoot: c.PropRoot, EdgeAdjRoot: c.EdgeAdjRoot,
		}, nil
	}
}
