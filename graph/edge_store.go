package graph

// EdgeStore abstracts edge storage and adjacency index operations.
// The Graph delegates edge CRUD and traversal queries to this interface.
//
// Implementations: MemoryEdgeStore (in-memory maps, current default),
// and BboltEdgeStore (bbolt-backed).
type EdgeStore interface {
	// Put stores an edge and updates adjacency indexes.
	Put(e *Edge)
	// Get retrieves an edge by ID.
	Get(id string) (*Edge, bool)
	// Delete removes an edge and updates adjacency indexes.
	Delete(id string)
	// From returns all outbound edges from a node.
	From(nodeID string) []*Edge
	// To returns all inbound edges to a node.
	To(nodeID string) []*Edge
	// ByType returns all edges of the given type.
	ByType(edgeType string) []*Edge
	// Count returns the total number of edges.
	Count() int
	// ForEach iterates all edges in unspecified order.
	ForEach(fn func(e *Edge))
	// Clear removes all edges and adjacency data. Used during Load
	// to reset state before repopulating from the prolly tree.
	Clear()
}

// MemoryEdgeStore is an in-memory EdgeStore using Go maps.
type MemoryEdgeStore struct {
	edges     map[string]*Edge
	outEdges  map[string]map[string]struct{} // source node ID -> edge IDs
	inEdges   map[string]map[string]struct{} // target node ID -> edge IDs
	typeEdges map[string]map[string]struct{} // edge type -> edge IDs
}

// NewMemoryEdgeStore creates an empty in-memory edge store.
func NewMemoryEdgeStore() *MemoryEdgeStore {
	return &MemoryEdgeStore{
		edges:     make(map[string]*Edge),
		outEdges:  make(map[string]map[string]struct{}),
		inEdges:   make(map[string]map[string]struct{}),
		typeEdges: make(map[string]map[string]struct{}),
	}
}

func (s *MemoryEdgeStore) Put(e *Edge) {
	s.edges[e.ID] = e
	addToIndex(s.outEdges, e.SourceID, e.ID)
	addToIndex(s.inEdges, e.TargetID, e.ID)
	addToIndex(s.typeEdges, e.Type, e.ID)
}

func (s *MemoryEdgeStore) Get(id string) (*Edge, bool) {
	e, ok := s.edges[id]
	return e, ok
}

func (s *MemoryEdgeStore) Delete(id string) {
	e, ok := s.edges[id]
	if !ok {
		return
	}
	removeFromIndex(s.outEdges, e.SourceID, id)
	removeFromIndex(s.inEdges, e.TargetID, id)
	removeFromIndex(s.typeEdges, e.Type, id)
	delete(s.edges, id)
}

func (s *MemoryEdgeStore) From(nodeID string) []*Edge {
	ids, ok := s.outEdges[nodeID]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, s.edges[eid])
	}
	return edges
}

func (s *MemoryEdgeStore) To(nodeID string) []*Edge {
	ids, ok := s.inEdges[nodeID]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, s.edges[eid])
	}
	return edges
}

func (s *MemoryEdgeStore) ByType(edgeType string) []*Edge {
	ids, ok := s.typeEdges[edgeType]
	if !ok {
		return nil
	}
	edges := make([]*Edge, 0, len(ids))
	for eid := range ids {
		edges = append(edges, s.edges[eid])
	}
	return edges
}

func (s *MemoryEdgeStore) Count() int {
	return len(s.edges)
}

func (s *MemoryEdgeStore) ForEach(fn func(e *Edge)) {
	for _, e := range s.edges {
		fn(e)
	}
}

func (s *MemoryEdgeStore) Clear() {
	s.edges = make(map[string]*Edge)
	s.outEdges = make(map[string]map[string]struct{})
	s.inEdges = make(map[string]map[string]struct{})
	s.typeEdges = make(map[string]map[string]struct{})
}

// OutEdgeIDs returns the raw set of edge IDs from a source node.
// Used by MarshalEdgeAdjacency and other internal operations that
// need the adjacency index directly.
func (s *MemoryEdgeStore) OutEdgeIDs() map[string]map[string]struct{} {
	return s.outEdges
}

// InEdgeIDs returns the raw set of edge IDs to a target node.
func (s *MemoryEdgeStore) InEdgeIDs() map[string]map[string]struct{} {
	return s.inEdges
}

// TypeEdgeIDs returns the raw set of edge IDs by type.
func (s *MemoryEdgeStore) TypeEdgeIDs() map[string]map[string]struct{} {
	return s.typeEdges
}
