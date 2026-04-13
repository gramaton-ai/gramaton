package graph

// NodeReader is the read interface for graph access. All read-side
// consumers should accept this interface rather than *Graph directly.
//
// The graph loads nodes lazily from the prolly tree with LRU caching.
// Callers that go through NodeReader work with any implementation
// unchanged.
type NodeReader interface {
	GetNode(id string) (*Node, bool)
	NodeIterator() NodeIterator
	NodeIDSet() map[string]struct{}
	NodeCount() int
	EdgeCount() int
	EdgesFrom(nodeID string) []*Edge
	EdgesTo(nodeID string) []*Edge
	IsStructuralChild(id string) bool
	SemanticEdgeCount(id string) int
}

// NodeIterator pages through nodes without full materialization.
// The current in-memory implementation wraps a slice. A future
// lazy implementation will cursor through the prolly tree.
//
// Usage:
//
//	it := g.NodeIterator()
//	defer it.Close()
//	for it.Next() {
//	    n := it.Node()
//	    // use n
//	}
type NodeIterator interface {
	Next() bool
	Node() *Node
	Close()
}

// compile-time check: *Graph satisfies NodeReader.
var _ NodeReader = (*Graph)(nil)
