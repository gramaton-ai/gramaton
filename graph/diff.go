package graph

// DiffResult describes the structural differences between two commits.
type DiffResult struct {
	// AddedNodes are node hashes present in the new commit but not the old.
	AddedNodes []string `json:"added_nodes,omitempty"`
	// RemovedNodes are node hashes present in the old commit but not the new.
	RemovedNodes []string `json:"removed_nodes,omitempty"`
	// AddedEdges are edge hashes present in the new commit but not the old.
	AddedEdges []string `json:"added_edges,omitempty"`
	// RemovedEdges are edge hashes present in the old commit but not the new.
	RemovedEdges []string `json:"removed_edges,omitempty"`
}

// DiffCommits computes the structural difference between two commits
// by comparing their hash lists. This is the cheap first step of the
// two-step diff process described in retrieval.md.
func DiffCommits(old, new *Commit) DiffResult {
	return DiffResult{
		AddedNodes:   setDiff(new.NodeHashes, old.NodeHashes),
		RemovedNodes: setDiff(old.NodeHashes, new.NodeHashes),
		AddedEdges:   setDiff(new.EdgeHashes, old.EdgeHashes),
		RemovedEdges: setDiff(old.EdgeHashes, new.EdgeHashes),
	}
}

// setDiff returns elements in a that are not in b.
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
