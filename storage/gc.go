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
}

// GCCommit is the minimal commit structure needed for GC.
// Avoids importing graph package to keep storage self-contained.
type GCCommit struct {
	Hash         string    `json:"hash"`
	Parent       string    `json:"parent,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	NodeTreeRoot string    `json:"node_tree_root,omitempty"`
	EdgeTreeRoot string    `json:"edge_tree_root,omitempty"`
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
		if err := s.markFromTip(tipHash, cutoff, opts.MinKeepCommits, reachable, opts.CommitLoader); err != nil {
			return nil, fmt.Errorf("storage gc: mark from tip %s: %w", tipHash[:12], err)
		}
	}

	result.ReachableCount = len(reachable)

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
func (s *Store) markFromTip(tipHash string, cutoff time.Time, minKeep int, reachable map[string]struct{}, loadCommit func(string) (*GCCommit, error)) error {
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
		s.markCommitChunks(commit, reachable)

		hash = commit.Parent
	}
	return nil
}

// markCommitChunks marks all CAS chunks referenced by a commit:
// prolly tree nodes (recursive), leaf data chunks, and index blobs.
func (s *Store) markCommitChunks(c *GCCommit, reachable map[string]struct{}) {
	// Walk prolly trees (nodes and edges).
	if c.NodeTreeRoot != "" {
		s.markProllyTree(c.NodeTreeRoot, reachable, 0)
	}
	if c.EdgeTreeRoot != "" {
		s.markProllyTree(c.EdgeTreeRoot, reachable, 0)
	}

	// Mark legacy index blobs (may be empty after sidecar migration).
	for _, root := range []string{
		c.BM25Root, c.BM25FullRoot, c.BM25MediumRoot, c.BM25ShortRoot,
		c.VecRoot, c.PropRoot, c.EdgeAdjRoot,
	} {
		if root != "" {
			reachable[root] = struct{}{}
		}
	}
}

// markProllyTree recursively walks a prolly tree node, marking all
// chunk hashes as reachable. For leaf nodes, it also marks leaf
// value hashes (individual node/edge data chunks).
func (s *Store) markProllyTree(hash string, reachable map[string]struct{}, depth int) {
	if depth > maxTreeDepth {
		return
	}
	if _, visited := reachable[hash]; visited {
		return
	}

	reachable[hash] = struct{}{}

	data, err := s.Read(hash)
	if err != nil {
		return // chunk missing or corrupt -- skip, don't crash GC
	}

	var node ProllyNode
	if err := json.Unmarshal(data, &node); err != nil {
		return // corrupt node -- skip
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
				s.markProllyTree(e.Value, reachable, depth+1)
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
