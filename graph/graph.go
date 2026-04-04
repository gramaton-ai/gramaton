package graph

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ErrNotFound is returned when a node or edge does not exist.
var ErrNotFound = errors.New("not found")

// Graph is an in-memory property graph engine. It holds nodes and edges,
// maintains bidirectional edge indexes, and enforces cascading deletion.
//
// The graph is not thread-safe. The server layer handles write serialization.
type Graph struct {
	nodes map[string]*Node
	edges map[string]*Edge

	// Edge indexes for efficient traversal.
	outEdges  map[string]map[string]struct{} // source node ID → set of edge IDs
	inEdges   map[string]map[string]struct{} // target node ID → set of edge IDs
	typeEdges map[string]map[string]struct{} // edge type → set of edge IDs

	entropy *ulid.MonotonicEntropy
	mu      sync.Mutex // protects entropy only
}

// New creates an empty graph.
func New() *Graph {
	return &Graph{
		nodes:     make(map[string]*Node),
		edges:     make(map[string]*Edge),
		outEdges:  make(map[string]map[string]struct{}),
		inEdges:   make(map[string]map[string]struct{}),
		typeEdges: make(map[string]map[string]struct{}),
		entropy:   ulid.Monotonic(rand.Reader, 0),
	}
}

// newID generates a new ULID.
func (g *Graph) newID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), g.entropy).String()
}

// addToIndex adds a value to a set-of-sets index.
func addToIndex(idx map[string]map[string]struct{}, key, val string) {
	s, ok := idx[key]
	if !ok {
		s = make(map[string]struct{})
		idx[key] = s
	}
	s[val] = struct{}{}
}

// removeFromIndex removes a value from a set-of-sets index.
func removeFromIndex(idx map[string]map[string]struct{}, key, val string) {
	s, ok := idx[key]
	if !ok {
		return
	}
	delete(s, val)
	if len(s) == 0 {
		delete(idx, key)
	}
}
