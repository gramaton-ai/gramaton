package graph

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/gramaton-ai/gramaton/storage"
	"github.com/oklog/ulid/v2"
)

// ErrNotFound is returned when a node or edge does not exist.
var ErrNotFound = errors.New("not found")

// Graph is a property graph engine backed by a content-addressed store.
// Nodes are lazily loaded from a prolly tree on first access and cached
// in memory. Edges are loaded at startup (MemoryEdgeStore) or on demand
// (BboltEdgeStore).
//
// The graph tracks which nodes and edges have been modified since the
// last save (dirty tracking), enabling incremental persistence. The
// nodeHashes/edgeHashes maps cache content hashes from the previous
// save, so only dirty items need re-marshaling.
//
// The graph is not thread-safe. The server layer handles write serialization.
type Graph struct {
	nodes map[string]*Node // in-memory node cache (lazy-loaded)
	edgeStore EdgeStore     // edge storage and adjacency indexes

	// Dirty tracking for incremental saves.
	dirtyNodes   map[string]struct{} // node IDs modified since last save
	dirtyEdges   map[string]struct{} // edge IDs modified since last save
	deletedNodes map[string]struct{} // node IDs deleted since last save
	deletedEdges map[string]struct{} // edge IDs deleted since last save

	// Content hash caches from previous save. Populated on Load()
	// and updated after each Save(). Used to skip re-marshaling
	// unchanged nodes/edges.
	nodeHashes map[string]string // node ID → content hash
	edgeHashes map[string]string // edge ID → content hash

	// Prolly tree roots from previous save, used for incremental
	// tree updates.
	lastNodeTreeRoot string
	lastEdgeTreeRoot string

	// Lazy loading support. When store is non-nil, GetNode falls
	// back to the prolly tree on cache miss. nodeTotal is the count
	// from the prolly tree (set during Load).
	store     *storage.Store
	nodeTotal int // total node count (prolly tree entries)
	lru       *lruTracker // LRU eviction for node cache

	entropy *ulid.MonotonicEntropy
	mu      sync.Mutex // protects entropy only

	// cacheMu protects g.nodes, g.nodeHashes, and the LRU tracker.
	// Read-locked for snapshot iteration (cachedIterator, cachedNodeIDs,
	// Save's full-node walk). Write-locked for AddNode, GetNode (always,
	// since LRU touch mutates), DeleteNode, eviction, and lazy load.
	//
	// This lock is independent of the engine RWMutex above it. Engine
	// callers must still serialize higher-level writes (multi-step
	// mutations like dedup, reembed) via engine.Lock(); cacheMu only
	// guarantees that the node map itself never observes a concurrent
	// read+write that would panic the runtime.
	cacheMu sync.RWMutex
}

// DefaultCacheCapacity is the default maximum number of nodes held in
// the in-memory cache. Nodes beyond this limit are evicted LRU. Set to
// 0 for unlimited (all accessed nodes stay cached). Dirty nodes are
// never evicted.
const DefaultCacheCapacity = 10000

// New creates an empty graph with the default cache capacity.
func New() *Graph {
	return NewWithCapacity(DefaultCacheCapacity)
}

// GraphOption configures graph creation.
type GraphOption func(*Graph)

// WithEdgeStore injects an external EdgeStore. If not provided,
// a MemoryEdgeStore is used (the default for backward compatibility).
func WithEdgeStore(es EdgeStore) GraphOption {
	return func(g *Graph) {
		g.edgeStore = es
	}
}

// NewWithCapacity creates a graph with a specific node cache capacity.
// 0 means unlimited.
func NewWithCapacity(cacheCapacity int, opts ...GraphOption) *Graph {
	g := &Graph{
		nodes:        make(map[string]*Node),
		edgeStore:    NewMemoryEdgeStore(),
		dirtyNodes:   make(map[string]struct{}),
		dirtyEdges:   make(map[string]struct{}),
		deletedNodes: make(map[string]struct{}),
		deletedEdges: make(map[string]struct{}),
		nodeHashes:   make(map[string]string),
		edgeHashes:   make(map[string]string),
		lru:          newLRUTracker(cacheCapacity),
		entropy:      ulid.Monotonic(rand.Reader, 0),
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// markNodeDirty marks a node as modified since the last save.
func (g *Graph) markNodeDirty(id string) {
	g.dirtyNodes[id] = struct{}{}
}

// markEdgeDirty marks an edge as modified since the last save.
func (g *Graph) markEdgeDirty(id string) {
	g.dirtyEdges[id] = struct{}{}
}

// ClearDirty resets all dirty tracking state. Called after a
// successful save.
func (g *Graph) ClearDirty() {
	g.dirtyNodes = make(map[string]struct{})
	g.dirtyEdges = make(map[string]struct{})
	g.deletedNodes = make(map[string]struct{})
	g.deletedEdges = make(map[string]struct{})
}

// IsDirty reports whether any nodes or edges have been modified
// since the last save.
func (g *Graph) IsDirty() bool {
	return len(g.dirtyNodes) > 0 || len(g.dirtyEdges) > 0 ||
		len(g.deletedNodes) > 0 || len(g.deletedEdges) > 0
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
