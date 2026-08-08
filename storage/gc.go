package storage

import (
	"encoding/json"
	"fmt"
	"time"
)

// GCResult summarizes what a garbage collection run found and did.
type GCResult struct {
	TotalChunks    int      `json:"total_chunks"`
	ReachableCount int      `json:"reachable_count"`
	DeletedCount   int      `json:"deleted_count"`
	DeletedHashes  []string `json:"deleted_hashes,omitempty"` // only in dry-run
	BytesFreed     int64    `json:"bytes_freed"`
	Errors         int      `json:"errors"`
}

// GCOptions controls garbage collection behavior.
type GCOptions struct {
	// DryRun reports what would be deleted without actually deleting.
	DryRun bool

	// MinAge is the minimum age for a commit to be eligible for GC.
	// Commits newer than this are always kept. Zero means keep all.
	MinAge time.Duration

	// MinKeepCommits is the minimum number of recent commits to
	// keep regardless of age. Protects against GC'ing all history
	// when a store is unused for a long time. Default: 5.
	MinKeepCommits int

	// CommitLoader reads a commit from its hash. Required.
	CommitLoader func(hash string) (*GCCommit, error)

	// BranchTips returns all branch tip commit hashes. Required.
	BranchTips func() []string

	// HeadHash is the current HEAD commit hash.
	HeadHash string

	// ExtraRoots are chunk hashes marked reachable as-is (no tree
	// walk): the retention tombstone, individually retained version
	// blobs.
	ExtraRoots []string

	// ExtraCommits are commit hashes marked like chain commits (the
	// chunk plus both trees) WITHOUT walking their parent chains: the
	// prune baseline, which is deliberately parentless-by-reference.
	ExtraCommits []string
}

// GCCommit is the minimal commit structure needed for GC.
// Avoids importing graph package to keep storage self-contained.
type GCCommit struct {
	Hash         string    `json:"hash"`
	Parent       string    `json:"parent,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	NodeTreeRoot string    `json:"node_tree_root,omitempty"`
	EdgeTreeRoot string    `json:"edge_tree_root,omitempty"`
	// PruneTombstoneRoot mirrors graph.Commit's field of the same
	// name: the retention-tombstone chunk a prune commit references.
	// Must be marked or the sweep collects the store's own record of
	// what it swept.
	PruneTombstoneRoot string `json:"prune_tombstone_root,omitempty"`
	// Legacy index roots (may be empty after sidecar migration).
	BM25Root       string `json:"bm25_root,omitempty"`
	BM25FullRoot   string `json:"bm25_full_root,omitempty"`
	BM25MediumRoot string `json:"bm25_medium_root,omitempty"`
	BM25ShortRoot  string `json:"bm25_short_root,omitempty"`
	VecRoot        string `json:"vec_root,omitempty"`
	PropRoot       string `json:"prop_root,omitempty"`
	EdgeAdjRoot    string `json:"edge_adj_root,omitempty"`
}

// GC performs mark-and-sweep garbage collection on the CAS store.
// It walks the commit graph from all branch tips, marks reachable
// chunks, then deletes (or reports) unreachable ones.
//
// Safety invariant: chunks reachable from ANY branch tip commit are
// never deleted, regardless of age.
func (s *Store) GC(opts GCOptions) (*GCResult, error) {
	if opts.CommitLoader == nil || opts.BranchTips == nil {
		return nil, fmt.Errorf("storage gc: CommitLoader and BranchTips are required")
	}
	if opts.MinKeepCommits <= 0 {
		opts.MinKeepCommits = 5
	}

	result := &GCResult{}

	// Phase 1: Mark reachable chunks by walking the commit graph.
	reachable := make(map[string]struct{})
	cutoff := time.Time{}
	if opts.MinAge > 0 {
		cutoff = time.Now().UTC().Add(-opts.MinAge)
	}

	tips := opts.BranchTips()
	if opts.HeadHash != "" {
		tips = appendUnique(tips, opts.HeadHash)
	}

	for _, tipHash := range tips {
		if err := s.markFromTip(tipHash, cutoff, opts.MinKeepCommits, reachable, opts.CommitLoader, result); err != nil {
			return nil, fmt.Errorf("storage gc: mark from tip %s: %w", tipHash[:12], err)
		}
	}

	for _, root := range opts.ExtraRoots {
		if root != "" {
			reachable[root] = struct{}{}
		}
	}
	for _, hash := range opts.ExtraCommits {
		if hash == "" {
			continue
		}
		reachable[hash] = struct{}{}
		c, err := opts.CommitLoader(hash)
		if err != nil {
			result.Errors++
			continue
		}
		s.markCommitChunks(c, reachable, result)
	}

	result.ReachableCount = len(reachable)

	// Refuse to sweep if marking incomplete: a corrupt or unreadable
	// tree subtree means some live chunks may be unmarked, and
	// proceeding to phase 2 would delete them. The operator should
	// investigate (gramaton verify, restore from backup) before
	// retrying GC. Phase 1 marking errors are recorded in
	// result.Errors so the caller can decide how to surface this.
	if result.Errors > 0 {
		return result, nil
	}

	// Phase 2: List all chunks and sweep unreachable ones.
	allHashes, err := s.List()
	if err != nil {
		return nil, fmt.Errorf("storage gc: list chunks: %w", err)
	}
	result.TotalChunks = len(allHashes)

	for _, hash := range allHashes {
		if _, ok := reachable[hash]; ok {
			continue // reachable, keep
		}

		if opts.DryRun {
			result.DeletedHashes = append(result.DeletedHashes, hash)
			result.DeletedCount++
			continue
		}

		if err := s.Delete(hash); err != nil {
			result.Errors++
			continue
		}
		result.DeletedCount++
	}

	return result, nil
}

// markFromTip walks the commit chain from a branch tip backward,
// marking all reachable CAS chunks. Stops when it reaches a commit
// older than the cutoff (after keeping at least minKeep commits).
// Tree-walk failures during marking are surfaced via result.Errors.
func (s *Store) markFromTip(tipHash string, cutoff time.Time, minKeep int, reachable map[string]struct{}, loadCommit func(string) (*GCCommit, error), result *GCResult) error {
	hash := tipHash
	kept := 0

	for hash != "" {
		if _, visited := reachable[hash]; visited {
			// Already visited this commit (shared ancestor of multiple branches).
			break
		}

		commit, err := loadCommit(hash)
		if err != nil {
			return fmt.Errorf("load commit %s: %w", hash[:12], err)
		}

		// Age check: stop after we've kept minKeep commits AND the
		// commit is older than the cutoff. Zero cutoff = keep all.
		if !cutoff.IsZero() && kept >= minKeep && commit.Timestamp.Before(cutoff) {
			break
		}

		// Mark the commit chunk itself.
		reachable[hash] = struct{}{}
		kept++

		// Mark all chunks reachable from this commit.
		s.markCommitChunks(commit, reachable, result)

		hash = commit.Parent
	}
	return nil
}

// markCommitChunks marks all CAS chunks referenced by a commit:
// prolly tree nodes (recursive), leaf data chunks, and index blobs.
// Tree-walk failures (unreadable or corrupt chunks) are surfaced as
// result.Errors++ so GC can refuse to sweep when marking is incomplete.
func (s *Store) markCommitChunks(c *GCCommit, reachable map[string]struct{}, result *GCResult) {
	// Walk prolly trees (nodes and edges).
	if c.NodeTreeRoot != "" {
		s.markProllyTree(c.NodeTreeRoot, reachable, 0, result)
	}
	if c.EdgeTreeRoot != "" {
		s.markProllyTree(c.EdgeTreeRoot, reachable, 0, result)
	}

	// Mark legacy index blobs (may be empty after sidecar migration)
	// and the retention tombstone, which is flat (no tree walk).
	for _, root := range []string{
		c.BM25Root, c.BM25FullRoot, c.BM25MediumRoot, c.BM25ShortRoot,
		c.VecRoot, c.PropRoot, c.EdgeAdjRoot, c.PruneTombstoneRoot,
	} {
		if root != "" {
			reachable[root] = struct{}{}
		}
	}
}

// markProllyTree recursively walks a prolly tree node, marking all
// chunk hashes as reachable. For leaf nodes, it also marks leaf
// value hashes (individual node/edge data chunks).
//
// Read or unmarshal failures increment result.Errors instead of
// crashing GC. The caller (GC) refuses to sweep when Errors > 0
// after the mark phase -- a corrupt subtree means we cannot safely
// distinguish reachable from unreachable, and proceeding would
// risk deleting live data.
func (s *Store) markProllyTree(hash string, reachable map[string]struct{}, depth int, result *GCResult) {
	if depth > maxTreeDepth {
		return
	}
	if _, visited := reachable[hash]; visited {
		return
	}

	reachable[hash] = struct{}{}

	data, err := s.Read(hash)
	if err != nil {
		result.Errors++
		return
	}

	var node ProllyNode
	if err := json.Unmarshal(data, &node); err != nil {
		result.Errors++
		return
	}

	if node.Leaf {
		// Leaf values are content hashes of individual nodes/edges.
		for _, e := range node.Entries {
			if e.Value != "" {
				reachable[e.Value] = struct{}{}
			}
		}
	} else {
		// Internal: values are child node hashes.
		for _, e := range node.Entries {
			if e.Value != "" {
				s.markProllyTree(e.Value, reachable, depth+1, result)
			}
		}
	}
}

func appendUnique(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}
