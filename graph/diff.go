package graph

import (
	"github.com/gramaton-ai/gramaton/storage"
)

// DiffResult describes the structural differences between two commits.
type DiffResult struct {
	// Added: entries in the new commit but not the old (key=node/edge ID, value=content hash).
	Added []storage.ProllyEntry `json:"added,omitempty"`
	// Removed: entries in the old commit but not the new.
	Removed []storage.ProllyEntry `json:"removed,omitempty"`
}

// DiffCommits computes the structural difference between two commits
// using prolly tree diff. For v1 commits, this efficiently skips
// unchanged subtrees. Falls back to flat list comparison for v0.
func DiffCommits(s *storage.Store, oldCommit, newCommit *Commit) (DiffResult, error) {
	var result DiffResult

	// Diff node trees.
	nodeAdded, nodeRemoved, err := diffTrees(s, oldCommit.NodeTreeRoot, newCommit.NodeTreeRoot,
		oldCommit.NodeHashes, newCommit.NodeHashes, oldCommit.Version, newCommit.Version)
	if err != nil {
		return result, err
	}
	result.Added = nodeAdded
	result.Removed = nodeRemoved

	return result, nil
}

func diffTrees(s *storage.Store, oldRoot, newRoot string, oldHashes, newHashes []string, oldVer, newVer int) ([]storage.ProllyEntry, []storage.ProllyEntry, error) {
	// Both v1: use prolly tree diff.
	if oldVer >= 1 && newVer >= 1 {
		oldTree := storage.LoadProllyTree(s, oldRoot)
		newTree := storage.LoadProllyTree(s, newRoot)
		return oldTree.Diff(newTree)
	}

	// Fallback for v0 commits: flat hash list comparison.
	added := setDiff(newHashes, oldHashes)
	removed := setDiff(oldHashes, newHashes)

	addedEntries := make([]storage.ProllyEntry, len(added))
	for i, h := range added {
		addedEntries[i] = storage.ProllyEntry{Value: h}
	}
	removedEntries := make([]storage.ProllyEntry, len(removed))
	for i, h := range removed {
		removedEntries[i] = storage.ProllyEntry{Value: h}
	}

	return addedEntries, removedEntries, nil
}

func setDiff(a, b []string) []string {
	bSet := make(map[string]struct{}, len(b))
	for _, h := range b {
		bSet[h] = struct{}{}
	}
	var result []string
	for _, h := range a {
		if _, ok := bSet[h]; !ok {
			result = append(result, h)
		}
	}
	return result
}
