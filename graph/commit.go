package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/brandonlattin/gramaton/storage"
)

// Commit is an immutable snapshot of the graph state.
type Commit struct {
	Version      int       `json:"version"`
	Hash         string    `json:"hash"`
	Parent       string    `json:"parent,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	Message      string    `json:"message"`
	NodeTreeRoot string    `json:"node_tree_root,omitempty"`
	EdgeTreeRoot string    `json:"edge_tree_root,omitempty"`
	// NodeHashes/EdgeHashes retained for reading v0 commits only.
	NodeHashes []string `json:"node_hashes,omitempty"`
	EdgeHashes []string `json:"edge_hashes,omitempty"`
}

// Save persists the current graph state as a commit to the store.
// Nodes and edges are stored as content-addressed chunks. Their IDs
// and chunk hashes are indexed in prolly trees for efficient diff.
func (g *Graph) Save(s *storage.Store, parent string, message string, pCfg ...storage.ProllyConfig) (*Commit, error) {
	var treeCfg storage.ProllyConfig
	if len(pCfg) > 0 {
		treeCfg = pCfg[0]
	}
	// Write all nodes and build the ID -> hash mapping.
	nodeMap := make(map[string]string, len(g.nodes))
	for _, id := range sortedNodeIDs(g) {
		n := g.nodes[id]
		data, err := MarshalNode(n)
		if err != nil {
			return nil, fmt.Errorf("save: marshal node %s: %w", id, err)
		}
		hash, err := s.Write(data)
		if err != nil {
			return nil, fmt.Errorf("save: write node %s: %w", id, err)
		}
		nodeMap[id] = hash
	}

	// Write all edges and build the ID -> hash mapping.
	edgeMap := make(map[string]string, len(g.edges))
	for _, id := range sortedEdgeIDs(g) {
		e := g.edges[id]
		data, err := MarshalEdge(e)
		if err != nil {
			return nil, fmt.Errorf("save: marshal edge %s: %w", id, err)
		}
		hash, err := s.Write(data)
		if err != nil {
			return nil, fmt.Errorf("save: write edge %s: %w", id, err)
		}
		edgeMap[id] = hash
	}

	// Build prolly trees.
	nodeTree := storage.NewProllyTree(s, treeCfg)
	if err := nodeTree.Build(storage.SortedEntries(nodeMap)); err != nil {
		return nil, fmt.Errorf("save: build node tree: %w", err)
	}

	edgeTree := storage.NewProllyTree(s, treeCfg)
	if err := edgeTree.Build(storage.SortedEntries(edgeMap)); err != nil {
		return nil, fmt.Errorf("save: build edge tree: %w", err)
	}

	commit := &Commit{
		Version:      1,
		Parent:       parent,
		Timestamp:    time.Now().UTC(),
		Message:      message,
		NodeTreeRoot: nodeTree.RootHash(),
		EdgeTreeRoot: edgeTree.RootHash(),
	}

	commitData, err := json.Marshal(commit)
	if err != nil {
		return nil, fmt.Errorf("save: marshal commit: %w", err)
	}
	commitHash, err := s.Write(commitData)
	if err != nil {
		return nil, fmt.Errorf("save: write commit: %w", err)
	}
	commit.Hash = commitHash

	return commit, nil
}

// Load restores graph state from a commit. Clears the current graph
// and replaces it with the committed state. Handles both v0 (flat
// hash lists) and v1 (prolly tree) commit formats.
func (g *Graph) Load(s *storage.Store, commitHash string) (*Commit, error) {
	commitData, err := s.Read(commitHash)
	if err != nil {
		return nil, fmt.Errorf("load: read commit %s: %w", commitHash, err)
	}
	var commit Commit
	if err := json.Unmarshal(commitData, &commit); err != nil {
		return nil, fmt.Errorf("load: unmarshal commit: %w", err)
	}
	commit.Hash = commitHash

	// Clear current state.
	g.nodes = make(map[string]*Node)
	g.edges = make(map[string]*Edge)
	g.outEdges = make(map[string]map[string]struct{})
	g.inEdges = make(map[string]map[string]struct{})
	g.typeEdges = make(map[string]map[string]struct{})

	// Collect node/edge hashes based on commit version.
	var nodeHashes []string
	var edgeHashes []string

	if commit.Version >= 1 && commit.NodeTreeRoot != "" {
		// v1: read from prolly tree.
		nodeTree := storage.LoadProllyTree(s, commit.NodeTreeRoot)
		entries, err := nodeTree.AllEntries()
		if err != nil {
			return nil, fmt.Errorf("load: read node tree: %w", err)
		}
		for _, e := range entries {
			nodeHashes = append(nodeHashes, e.Value)
		}

		if commit.EdgeTreeRoot != "" {
			edgeTree := storage.LoadProllyTree(s, commit.EdgeTreeRoot)
			entries, err := edgeTree.AllEntries()
			if err != nil {
				return nil, fmt.Errorf("load: read edge tree: %w", err)
			}
			for _, e := range entries {
				edgeHashes = append(edgeHashes, e.Value)
			}
		}
	} else {
		// v0: flat hash lists.
		nodeHashes = commit.NodeHashes
		edgeHashes = commit.EdgeHashes
	}

	// Load nodes.
	for _, hash := range nodeHashes {
		data, err := s.Read(hash)
		if err != nil {
			return nil, fmt.Errorf("load: read node chunk %s: %w", hash, err)
		}
		n, err := UnmarshalNode(data)
		if err != nil {
			return nil, fmt.Errorf("load: unmarshal node: %w", err)
		}
		g.nodes[n.ID] = n
	}

	// Load edges and rebuild indexes.
	for _, hash := range edgeHashes {
		data, err := s.Read(hash)
		if err != nil {
			return nil, fmt.Errorf("load: read edge chunk %s: %w", hash, err)
		}
		e, err := UnmarshalEdge(data)
		if err != nil {
			return nil, fmt.Errorf("load: unmarshal edge: %w", err)
		}
		g.edges[e.ID] = e
		addToIndex(g.outEdges, e.SourceID, e.ID)
		addToIndex(g.inEdges, e.TargetID, e.ID)
		addToIndex(g.typeEdges, e.Type, e.ID)
	}

	return &commit, nil
}

// NodeIDsInCommit returns all node IDs in a commit without loading
// the full graph. Uses the prolly tree for v1 commits, falls back
// to reading all chunks for v0 commits.
func NodeIDsInCommit(s *storage.Store, commitHash string) ([]string, error) {
	commitData, err := s.Read(commitHash)
	if err != nil {
		return nil, err
	}
	var commit Commit
	if err := json.Unmarshal(commitData, &commit); err != nil {
		return nil, err
	}

	if commit.Version >= 1 && commit.NodeTreeRoot != "" {
		tree := storage.LoadProllyTree(s, commit.NodeTreeRoot)
		entries, err := tree.AllEntries()
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(entries))
		for i, e := range entries {
			ids[i] = e.Key
		}
		return ids, nil
	}

	// v0 fallback: read each chunk to get the node ID.
	var ids []string
	for _, hash := range commit.NodeHashes {
		data, err := s.Read(hash)
		if err != nil {
			continue
		}
		n, err := UnmarshalNode(data)
		if err != nil {
			continue
		}
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// NodeHashInCommit returns the content hash for a specific node ID
// in a commit. Uses prolly tree lookup for v1 commits (O(log N)).
func NodeHashInCommit(s *storage.Store, commitHash, nodeID string) (string, bool, error) {
	commitData, err := s.Read(commitHash)
	if err != nil {
		return "", false, err
	}
	var commit Commit
	if err := json.Unmarshal(commitData, &commit); err != nil {
		return "", false, err
	}

	if commit.Version >= 1 && commit.NodeTreeRoot != "" {
		tree := storage.LoadProllyTree(s, commit.NodeTreeRoot)
		hash, ok := tree.Get(nodeID)
		return hash, ok, nil
	}

	// v0 fallback: read each chunk.
	for _, hash := range commit.NodeHashes {
		data, err := s.Read(hash)
		if err != nil {
			continue
		}
		n, err := UnmarshalNode(data)
		if err != nil {
			continue
		}
		if n.ID == nodeID {
			return hash, true, nil
		}
	}
	return "", false, nil
}

func sortedNodeIDs(g *Graph) []string {
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedEdgeIDs(g *Graph) []string {
	ids := make([]string, 0, len(g.edges))
	for id := range g.edges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
