package graph

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/storage"
)

// Node is a vertex in the property graph. It has a stable ULID and a
// set of typed properties. The graph engine treats all nodes identically --
// "types" like knowledge record, concept node, and chunk node are
// conventions enforced by higher layers through property values.
type Node struct {
	ID         string
	Properties Properties
}

// AddNode creates a new node with the given properties and returns it.
// The node is assigned a new ULID. Properties are cloned on creation.
func (g *Graph) AddNode(props Properties) *Node {
	n := &Node{
		ID:         g.newID(),
		Properties: props.Clone(),
	}
	if n.Properties == nil {
		n.Properties = make(Properties)
	}
	g.nodes[n.ID] = n
	g.nodeTotal++
	g.markNodeDirty(n.ID)
	return n
}

// GetNode returns the node with the given ID, or nil and false if not found.
// When the graph has a backing store (after Load), cache misses trigger a
// lazy load from the prolly tree. Accessed nodes are promoted in the LRU;
// eviction may remove a clean (non-dirty) node from the cache.
func (g *Graph) GetNode(id string) (*Node, bool) {
	g.cacheMu.Lock()
	if n, ok := g.nodes[id]; ok {
		g.evictLRU(id)
		g.cacheMu.Unlock()
		return n, true
	}
	// Lazy load from prolly tree if we have a backing store.
	if g.store != nil && g.lastNodeTreeRoot != "" {
		n, err := g.loadNode(id)
		if err != nil || n == nil {
			g.cacheMu.Unlock()
			return nil, false
		}
		g.nodes[id] = n
		g.evictLRU(id)
		g.cacheMu.Unlock()
		return n, true
	}
	g.cacheMu.Unlock()
	return nil, false
}

// evictLRU promotes id in the LRU tracker and evicts the least recently
// used clean node if the cache exceeds capacity. Dirty nodes are never
// evicted (they must be written first).
func (g *Graph) evictLRU(id string) {
	if g.lru == nil {
		return
	}
	evictedID, evicted := g.lru.touch(id)
	if !evicted {
		return
	}
	// Don't evict dirty nodes -- they haven't been saved yet.
	if _, dirty := g.dirtyNodes[evictedID]; dirty {
		// Put it back in the tracker so it's not lost.
		g.lru.touch(evictedID)
		return
	}
	delete(g.nodes, evictedID)
}

// loadNode fetches a single node from the prolly tree by ID.
func (g *Graph) loadNode(id string) (*Node, error) {
	tree := storage.LoadProllyTree(g.store, g.lastNodeTreeRoot)
	hash, ok := tree.Get(id)
	if !ok {
		return nil, nil
	}
	data, err := g.store.Read(hash)
	if err != nil {
		return nil, err
	}
	n, err := UnmarshalNode(data)
	if err != nil {
		return nil, err
	}
	// Cache the content hash for incremental saves.
	g.nodeHashes[id] = hash
	return n, nil
}

// SetNodeProperty sets a single property on a node. Creates the property
// if absent, overwrites if present. Lazily loads the node if needed.
func (g *Graph) SetNodeProperty(id, key string, val Property) error {
	n, ok := g.GetNode(id)
	if !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}
	n.Properties[key] = val
	g.markNodeDirty(id)
	return nil
}

// RemoveNodeProperty removes a property from a node. No error if the
// property doesn't exist. Lazily loads the node if needed.
func (g *Graph) RemoveNodeProperty(id, key string) error {
	n, ok := g.GetNode(id)
	if !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}
	delete(n.Properties, key)
	g.markNodeDirty(id)
	return nil
}

// DeleteNode removes a node and all its inbound and outbound edges
// (cascading deletion). Returns ErrNotFound if the node doesn't exist.
// Lazily loads the node if needed.
func (g *Graph) DeleteNode(id string) error {
	if _, ok := g.GetNode(id); !ok {
		return fmt.Errorf("graph: node %s: %w", id, ErrNotFound)
	}

	// Collect unique edge IDs to delete (outbound + inbound).
	// A self-edge appears in both sets, so we deduplicate.
	edgeSet := make(map[string]struct{})
	for _, e := range g.edgeStore.From(id) {
		edgeSet[e.ID] = struct{}{}
	}
	for _, e := range g.edgeStore.To(id) {
		edgeSet[e.ID] = struct{}{}
	}

	// Delete edges first (updates all indexes).
	for eid := range edgeSet {
		g.deleteEdge(eid)
	}

	delete(g.nodes, id)
	delete(g.dirtyNodes, id)
	g.deletedNodes[id] = struct{}{}
	g.nodeTotal--
	if g.lru != nil {
		g.lru.removeID(id)
	}
	return nil
}

// NodeCount returns the number of nodes in the graph. In lazy mode,
// this returns the count from the prolly tree (which includes nodes
// not yet loaded into the cache).
func (g *Graph) NodeCount() int {
	if g.nodeTotal > 0 {
		return g.nodeTotal
	}
	return len(g.nodes)
}

// NodeIDSet returns a set of all node IDs without loading node data.
// Uses the in-memory nodeHashes map (populated at Load time) so this
// is O(n) in IDs only, no disk I/O.
func (g *Graph) NodeIDSet() map[string]struct{} {
	result := make(map[string]struct{}, len(g.nodeHashes)+len(g.nodes))
	for id := range g.nodeHashes {
		result[id] = struct{}{}
	}
	// Include any nodes added after Load (in-memory only, not yet committed).
	for id := range g.nodes {
		result[id] = struct{}{}
	}
	return result
}

// AllNodeIDs returns all node IDs in the graph. Order is not guaranteed.
// In lazy mode, iterates the prolly tree for IDs.
//
// Prefer NodeIterator for new code -- it avoids the intermediate slice.
func (g *Graph) AllNodeIDs() []string {
	if g.store != nil && g.lastNodeTreeRoot != "" {
		tree := storage.LoadProllyTree(g.store, g.lastNodeTreeRoot)
		entries, err := tree.AllEntries()
		if err != nil {
			// Fall back to cache-only.
			return g.cachedNodeIDs()
		}
		ids := make([]string, len(entries))
		for i, e := range entries {
			ids[i] = e.Key
		}
		return ids
	}
	return g.cachedNodeIDs()
}

func (g *Graph) cachedNodeIDs() []string {
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	return ids
}

// NodeIterator returns an iterator over all nodes in the graph.
// In lazy mode, iterates prolly tree entries and loads each node on
// demand. In eager mode (no backing store), iterates the in-memory map.
func (g *Graph) NodeIterator() NodeIterator {
	if g.store != nil && g.lastNodeTreeRoot != "" {
		tree := storage.LoadProllyTree(g.store, g.lastNodeTreeRoot)
		entries, err := tree.AllEntries()
		if err != nil {
			return g.cachedIterator()
		}
		return &lazyNodeIterator{g: g, entries: entries, pos: -1}
	}
	return g.cachedIterator()
}

func (g *Graph) cachedIterator() NodeIterator {
	nodes := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, n)
	}
	return &sliceNodeIterator{nodes: nodes, pos: -1}
}

// lazyNodeIterator iterates prolly tree entries, loading each node
// via GetNode (which checks the cache first).
type lazyNodeIterator struct {
	g       *Graph
	entries []storage.ProllyEntry
	pos     int
	current *Node
}

func (it *lazyNodeIterator) Next() bool {
	for {
		it.pos++
		if it.pos >= len(it.entries) {
			return false
		}
		id := it.entries[it.pos].Key
		n, ok := it.g.GetNode(id)
		if ok {
			it.current = n
			return true
		}
		// Node missing from tree (shouldn't happen, but skip).
	}
}

func (it *lazyNodeIterator) Node() *Node {
	return it.current
}

func (it *lazyNodeIterator) Close() {
	it.entries = nil
	it.current = nil
}

// sliceNodeIterator is the in-memory NodeIterator backed by a slice.
type sliceNodeIterator struct {
	nodes []*Node
	pos   int
}

func (it *sliceNodeIterator) Next() bool {
	it.pos++
	return it.pos < len(it.nodes)
}

func (it *sliceNodeIterator) Node() *Node {
	return it.nodes[it.pos]
}

func (it *sliceNodeIterator) Close() {
	it.nodes = nil
}
