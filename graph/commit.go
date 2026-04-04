package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Commit is an immutable snapshot of the graph state.
type Commit struct {
	Hash      string    `json:"hash"`
	Parent    string    `json:"parent,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	// NodeHashes and EdgeHashes are the content-addressed hashes of all
	// nodes and edges in the graph at this commit. Flat hash lists (D19).
	NodeHashes []string `json:"node_hashes"`
	EdgeHashes []string `json:"edge_hashes"`
}

// store is the consumer-defined interface for what the graph needs
// from content-addressed storage.
type store interface {
	Write(data []byte) (string, error)
	Read(hash string) ([]byte, error)
	Has(hash string) bool
}

// Save persists the current graph state as a commit to the store.
// Returns the commit. Each node and edge is written as a separate
// content-addressed chunk. The commit itself is also a chunk.
func (g *Graph) Save(s store, parent string, message string) (*Commit, error) {
	// Write all nodes.
	nodeHashes := make([]string, 0, len(g.nodes))
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
		nodeHashes = append(nodeHashes, hash)
	}

	// Write all edges.
	edgeHashes := make([]string, 0, len(g.edges))
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
		edgeHashes = append(edgeHashes, hash)
	}

	// Build and write the commit.
	commit := &Commit{
		Parent:     parent,
		Timestamp:  time.Now().UTC(),
		Message:    message,
		NodeHashes: nodeHashes,
		EdgeHashes: edgeHashes,
	}

	commitData, err := marshalCommit(commit)
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
// and replaces it with the committed state.
func (g *Graph) Load(s store, commitHash string) (*Commit, error) {
	// Read and parse the commit.
	commitData, err := s.Read(commitHash)
	if err != nil {
		return nil, fmt.Errorf("load: read commit %s: %w", commitHash, err)
	}
	commit, err := unmarshalCommit(commitData)
	if err != nil {
		return nil, fmt.Errorf("load: unmarshal commit: %w", err)
	}
	commit.Hash = commitHash

	// Clear current state.
	g.nodes = make(map[string]*Node)
	g.edges = make(map[string]*Edge)
	g.outEdges = make(map[string]map[string]struct{})
	g.inEdges = make(map[string]map[string]struct{})
	g.typeEdges = make(map[string]map[string]struct{})

	// Load nodes.
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
	}

	// Load edges and rebuild indexes.
	for _, hash := range commit.EdgeHashes {
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

	return commit, nil
}

// marshalCommit encodes a commit as JSON. Using JSON here (not binary)
// because commits are small and human-inspectability is valuable.
func marshalCommit(c *Commit) ([]byte, error) {
	return json.Marshal(c)
}

func unmarshalCommit(data []byte) (*Commit, error) {
	var c Commit
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// HeadFile is the filename that stores the current HEAD commit hash.
const HeadFile = "HEAD"

// SaveHead writes the current HEAD commit hash to the store as a
// well-known key. This is how we find the latest state on startup.
func SaveHead(s store, commitHash string) error {
	// Write HEAD as a simple file. We use a separate mechanism from
	// content-addressed storage since HEAD is mutable.
	_, err := s.Write([]byte("HEAD:" + commitHash))
	return err
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
