package graph

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/gramaton-ai/gramaton/storage"
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
	// Persisted index roots (content-addressed chunks).
	// Omitempty ensures backward compatibility with older commits.
	BM25Root       string `json:"bm25_root,omitempty"`        // legacy single-index (read as bm25Full)
	BM25FullRoot   string `json:"bm25_full_root,omitempty"`   // content_full BM25 index
	BM25MediumRoot string `json:"bm25_medium_root,omitempty"` // content_medium BM25 index
	BM25ShortRoot  string `json:"bm25_short_root,omitempty"`  // content_short BM25 index
	VecRoot        string `json:"vec_root,omitempty"`
	PropRoot       string `json:"prop_root,omitempty"`
	EdgeAdjRoot    string `json:"edge_adj_root,omitempty"`
	// NodeHashes/EdgeHashes retained for reading v0 commits only.
	NodeHashes []string `json:"node_hashes,omitempty"`
	EdgeHashes []string `json:"edge_hashes,omitempty"`
}

// Save persists the current graph state as a commit to the store.
// Uses dirty tracking to only marshal modified nodes/edges. The
// full entry maps (nodeHashes/edgeHashes) are rebuilt for the
// prolly tree, but unchanged chunks deduplicate via content-addressing.
func (g *Graph) Save(s *storage.Store, parent string, message string, pCfg ...storage.ProllyConfig) (*Commit, error) {
	var treeCfg storage.ProllyConfig
	if len(pCfg) > 0 {
		treeCfg = pCfg[0]
	}

	// Determine if we can use incremental save (have cached hashes
	// from a previous save/load) or need a full save.
	incremental := len(g.nodeHashes) > 0 || len(g.edgeHashes) > 0

	if incremental {
		// Only marshal dirty nodes; reuse cached hashes for clean ones.
		for id := range g.dirtyNodes {
			n, ok := g.nodes[id]
			if !ok {
				continue // deleted after being marked dirty
			}
			data, err := MarshalNode(n)
			if err != nil {
				return nil, fmt.Errorf("save: marshal node %s: %w", id, err)
			}
			hash, err := s.Write(data)
			if err != nil {
				return nil, fmt.Errorf("save: write node %s: %w", id, err)
			}
			g.nodeHashes[id] = hash
		}
		// Remove deleted nodes from hash cache.
		for id := range g.deletedNodes {
			delete(g.nodeHashes, id)
		}

		// Same for edges.
		for id := range g.dirtyEdges {
			e, ok := g.edgeStore.Get(id)
			if !ok {
				continue
			}
			data, err := MarshalEdge(e)
			if err != nil {
				return nil, fmt.Errorf("save: marshal edge %s: %w", id, err)
			}
			hash, err := s.Write(data)
			if err != nil {
				return nil, fmt.Errorf("save: write edge %s: %w", id, err)
			}
			g.edgeHashes[id] = hash
		}
		for id := range g.deletedEdges {
			delete(g.edgeHashes, id)
		}
	} else {
		// Full save: marshal all nodes and edges (first save or after
		// branch switch). Populates the hash caches for future
		// incremental saves.
		g.nodeHashes = make(map[string]string, len(g.nodes))
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
			g.nodeHashes[id] = hash
		}

		g.edgeHashes = make(map[string]string, g.edgeStore.Count())
		for _, id := range sortedEdgeIDs(g) {
			e, _ := g.edgeStore.Get(id)
			data, err := MarshalEdge(e)
			if err != nil {
				return nil, fmt.Errorf("save: marshal edge %s: %w", id, err)
			}
			hash, err := s.Write(data)
			if err != nil {
				return nil, fmt.Errorf("save: write edge %s: %w", id, err)
			}
			g.edgeHashes[id] = hash
		}
	}

	// Update prolly trees. Use incremental Update() when a previous
	// tree root exists (touches only affected chunks -- O(K*depth)).
	// Fall back to full Build() for the first save or after branch switch.
	var nodeTreeRoot, edgeTreeRoot string

	if incremental && g.lastNodeTreeRoot != "" {
		// Incremental: collect mutations from dirty/deleted sets.
		var nodeMuts []storage.ProllyMutation
		for id := range g.dirtyNodes {
			if h, ok := g.nodeHashes[id]; ok {
				nodeMuts = append(nodeMuts, storage.ProllyMutation{Key: id, Value: h})
			}
		}
		for id := range g.deletedNodes {
			nodeMuts = append(nodeMuts, storage.ProllyMutation{Key: id, Delete: true})
		}

		nodeTree := storage.LoadProllyTree(s, g.lastNodeTreeRoot)
		nodeTree.SetConfig(treeCfg)
		if err := nodeTree.Update(nodeMuts); err != nil {
			return nil, fmt.Errorf("save: update node tree: %w", err)
		}
		nodeTreeRoot = nodeTree.RootHash()
	} else {
		nodeTree := storage.NewProllyTree(s, treeCfg)
		if err := nodeTree.Build(storage.SortedEntries(g.nodeHashes)); err != nil {
			return nil, fmt.Errorf("save: build node tree: %w", err)
		}
		nodeTreeRoot = nodeTree.RootHash()
	}

	if incremental && g.lastEdgeTreeRoot != "" {
		var edgeMuts []storage.ProllyMutation
		for id := range g.dirtyEdges {
			if h, ok := g.edgeHashes[id]; ok {
				edgeMuts = append(edgeMuts, storage.ProllyMutation{Key: id, Value: h})
			}
		}
		for id := range g.deletedEdges {
			edgeMuts = append(edgeMuts, storage.ProllyMutation{Key: id, Delete: true})
		}

		edgeTree := storage.LoadProllyTree(s, g.lastEdgeTreeRoot)
		edgeTree.SetConfig(treeCfg)
		if err := edgeTree.Update(edgeMuts); err != nil {
			return nil, fmt.Errorf("save: update edge tree: %w", err)
		}
		edgeTreeRoot = edgeTree.RootHash()
	} else {
		edgeTree := storage.NewProllyTree(s, treeCfg)
		if err := edgeTree.Build(storage.SortedEntries(g.edgeHashes)); err != nil {
			return nil, fmt.Errorf("save: build edge tree: %w", err)
		}
		edgeTreeRoot = edgeTree.RootHash()
	}

	g.lastNodeTreeRoot = nodeTreeRoot
	g.lastEdgeTreeRoot = edgeTreeRoot

	commit := &Commit{
		Version:      1,
		Parent:       parent,
		Timestamp:    time.Now().UTC(),
		Message:      message,
		NodeTreeRoot: nodeTreeRoot,
		EdgeTreeRoot: edgeTreeRoot,
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

	// Clear dirty tracking after successful save.
	g.ClearDirty()

	return commit, nil
}

// LoadCommitMeta reads a commit's JSON from storage without mutating
// graph state. Used by callers that only need commit-level fields
// (parent, timestamp, index roots) -- chain traversal, timestamp
// backfill, history walkers -- and want to avoid the prolly-tree
// load that full (*Graph).Load performs.
func LoadCommitMeta(s *storage.Store, commitHash string) (*Commit, error) {
	data, err := s.Read(commitHash)
	if err != nil {
		return nil, fmt.Errorf("load commit meta %s: %w", commitHash, err)
	}
	var c Commit
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unmarshal commit %s: %w", commitHash, err)
	}
	c.Hash = commitHash
	return &c, nil
}

// RewriteCommit re-serializes a commit with updated fields (e.g.,
// index roots) and returns the new commit with updated hash. The
// old commit chunk remains in the store but is unreferenced.
func RewriteCommit(s *storage.Store, c *Commit) (*Commit, error) {
	c.Hash = "" // clear so it doesn't affect serialization
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("rewrite commit: marshal: %w", err)
	}
	hash, err := s.Write(data)
	if err != nil {
		return nil, fmt.Errorf("rewrite commit: write: %w", err)
	}
	c.Hash = hash
	return c, nil
}

// Load restores graph state from a commit. For v1 commits, nodes are
// NOT loaded eagerly -- they are lazily loaded from the prolly tree on
// first access via GetNode. Edges are fully loaded since they're
// lightweight and needed for adjacency indexes.
//
// Handles both v0 (flat hash lists) and v1 (prolly tree) commit formats.
// v0 commits still load everything eagerly since they lack prolly tree
// support for single-key lookup.
func (g *Graph) Load(s *storage.Store, commitHash string) (*Commit, error) {
	loadStart := time.Now()
	slog.Info("loading graph from commit",
		"component", "graph",
		"commit", commitHash[:12])

	commitData, err := s.Read(commitHash)
	if err != nil {
		return nil, fmt.Errorf("load: read commit %s: %w", commitHash, err)
	}
	var commit Commit
	if err := json.Unmarshal(commitData, &commit); err != nil {
		return nil, fmt.Errorf("load: unmarshal commit: %w", err)
	}
	commit.Hash = commitHash

	// Check if bbolt edge store is already populated before clearing.
	// If populated, we skip the expensive per-edge Put reload.
	edgeStorePopulated := false
	if bbes, ok := g.edgeStore.(*BboltEdgeStore); ok {
		edgeStorePopulated = bbes.Count() > 0
	}

	// Clear in-memory state. Only clear bbolt edge store if we need
	// to reload edges from the prolly tree.
	g.nodes = make(map[string]*Node)
	if !edgeStorePopulated {
		g.edgeStore.Clear()
	}
	g.nodeHashes = make(map[string]string)
	g.edgeHashes = make(map[string]string)
	g.store = s
	g.nodeTotal = 0
	g.ClearDirty()

	type idHash struct {
		id, hash string
	}
	var edgeEntries []idHash

	if commit.Version >= 1 && commit.NodeTreeRoot != "" {
		g.lastNodeTreeRoot = commit.NodeTreeRoot

		// Lazy loading: don't load nodes. Just count them and store
		// the tree root for on-demand access via GetNode.
		nodeTree := storage.LoadProllyTree(s, commit.NodeTreeRoot)
		nodeCount, err := nodeTree.EntryCount()
		if err != nil {
			return nil, fmt.Errorf("load: count node tree: %w", err)
		}
		g.nodeTotal = nodeCount

		// Populate nodeHashes from prolly tree entries so incremental
		// saves know about all existing nodes (even uncached ones).
		entries, err := nodeTree.AllEntries()
		if err != nil {
			return nil, fmt.Errorf("load: read node tree entries: %w", err)
		}
		for _, e := range entries {
			g.nodeHashes[e.Key] = e.Value
		}
		slog.Info("node tree loaded",
			"component", "graph",
			"nodes", g.nodeTotal,
			"elapsed", time.Since(loadStart).Round(time.Millisecond).String())

		if commit.EdgeTreeRoot != "" {
			g.lastEdgeTreeRoot = commit.EdgeTreeRoot
			edgeTree := storage.LoadProllyTree(s, commit.EdgeTreeRoot)
			entries, err := edgeTree.AllEntries()
			if err != nil {
				return nil, fmt.Errorf("load: read edge tree: %w", err)
			}
			for _, e := range entries {
				edgeEntries = append(edgeEntries, idHash{e.Key, e.Value})
			}
		}
	} else {
		// v0: flat hash lists -- no prolly tree, must load all nodes eagerly.
		for _, hash := range commit.NodeHashes {
			data, err := s.Read(hash)
			if err != nil {
				return nil, fmt.Errorf("load: read node chunk %s: %w", hash, err)
			}
			n, err := UnmarshalNode(data)
			if err != nil {
				return nil, fmt.Errorf("load: unmarshal node: %w", err)
			}
			g.nodes[n.ID] = n
			g.nodeHashes[n.ID] = hash
		}
		g.nodeTotal = len(g.nodes)

		for _, hash := range commit.EdgeHashes {
			edgeEntries = append(edgeEntries, idHash{"", hash})
		}
	}

	// Load edges. If the bbolt edge store already has data (checked
	// above before Clear), skip the expensive per-edge Put calls.
	if edgeStorePopulated {
		slog.Info("edge store already populated, skipping load from prolly tree",
			"component", "graph",
			"edges", len(edgeEntries))
		for _, eh := range edgeEntries {
			g.edgeHashes[eh.id] = eh.hash
		}
	} else {
		slog.Info("loading edges into bbolt",
			"component", "graph",
			"edges", len(edgeEntries))
		for _, eh := range edgeEntries {
			data, err := s.Read(eh.hash)
			if err != nil {
				return nil, fmt.Errorf("load: read edge chunk %s: %w", eh.hash, err)
			}
			e, err := UnmarshalEdge(data)
			if err != nil {
				return nil, fmt.Errorf("load: unmarshal edge: %w", err)
			}
			g.edgeStore.Put(e)
			g.edgeHashes[e.ID] = eh.hash
		}
	}

	slog.Info("graph load complete",
		"component", "graph",
		"nodes", g.nodeTotal,
		"edges", len(edgeEntries),
		"edge_store_skipped", edgeStorePopulated,
		"elapsed", time.Since(loadStart).Round(time.Millisecond).String())

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
	ids := make([]string, 0, g.edgeStore.Count())
	g.edgeStore.ForEach(func(e *Edge) {
		ids = append(ids, e.ID)
	})
	sort.Strings(ids)
	return ids
}
