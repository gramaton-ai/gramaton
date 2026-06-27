package index

import (
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
	bolt "go.etcd.io/bbolt"
)

// PropertyIndex supports exact match, range, and substring queries over
// node properties. Implementations must be kept in sync with the graph
// via Add/Remove calls.
//
// Implementations: MemoryPropertyIndex (in-memory maps),
// and BboltPropertyIndex (bbolt-backed).
type PropertyIndex interface {
	// Add indexes a property value for a node via its own tx (or
	// in-memory write). Use AddTx inside a shared transaction to
	// amortize fsync.
	Add(nodeID, key string, val graph.Property)
	// AddTx indexes a property value via the caller's tx. In-memory
	// implementations ignore the tx argument.
	AddTx(tx *bolt.Tx, nodeID, key string, val graph.Property)
	// Remove removes a specific property value via its own tx.
	Remove(nodeID, key string, val graph.Property)
	// RemoveTx removes a property value via the caller's tx.
	RemoveTx(tx *bolt.Tx, nodeID, key string, val graph.Property)
	// RemoveNode removes all indexed properties for a node via its own tx.
	RemoveNode(nodeID string, props graph.Properties)
	// RemoveNodeTx removes all indexed properties via the caller's tx.
	RemoveNodeTx(tx *bolt.Tx, nodeID string, props graph.Properties)
	// Lookup returns all node IDs with an exact property match.
	Lookup(key string, val graph.Property) []string
	// Contains returns all node IDs where the string property contains the substring (case-sensitive).
	Contains(key, substring string) []string
	// ContainsFold returns all node IDs where the string property contains the substring (case-insensitive).
	ContainsFold(key, substring string) []string
	// LookupKeyword returns all node IDs where the StringList property contains the keyword.
	LookupKeyword(key, keyword string) []string
	// NodesWithKey returns all node IDs that have the given key indexed.
	NodesWithKey(key string) map[string]struct{}
	// KeywordCounts returns keyword -> count for all keywords under the given key.
	KeywordCounts(key string) map[string]int
	// Count returns the total number of indexed entries across all keys.
	Count() int
}

// MemoryPropertyIndex is an in-memory implementation of PropertyIndex
// using Go maps. Not thread-safe -- the server layer handles serialization.
type MemoryPropertyIndex struct {
	// Exact match: key → serialized value → set of node IDs.
	exact map[string]map[string]map[string]struct{}

	// Substring search: key → node ID → string value (string properties only).
	strings map[string]map[string]string

	// Keyword index: key → keyword string → set of node IDs.
	// Built from StringList properties, indexing each element individually.
	keywords map[string]map[string]map[string]struct{}

	// Reverse index: node ID → set of keys indexed for that node.
	// Used by RemoveNode to clean up all entries for a deleted node.
	nodeKeys map[string]map[string]struct{}
}

// NewPropertyIndex creates an empty in-memory property index.
// Deprecated: Use NewMemoryPropertyIndex for clarity. This alias
// exists for backward compatibility during the interface migration.
func NewPropertyIndex() *MemoryPropertyIndex {
	return NewMemoryPropertyIndex()
}

// NewMemoryPropertyIndex creates an empty in-memory property index.
func NewMemoryPropertyIndex() *MemoryPropertyIndex {
	return &MemoryPropertyIndex{
		exact:    make(map[string]map[string]map[string]struct{}),
		strings:  make(map[string]map[string]string),
		keywords: make(map[string]map[string]map[string]struct{}),
		nodeKeys: make(map[string]map[string]struct{}),
	}
}

// Add indexes a property value for a node. Call this when a node is
// created or a property is set.
func (idx *MemoryPropertyIndex) Add(nodeID, key string, val graph.Property) {
	// Track which keys this node has indexed.
	if _, ok := idx.nodeKeys[nodeID]; !ok {
		idx.nodeKeys[nodeID] = make(map[string]struct{})
	}
	idx.nodeKeys[nodeID][key] = struct{}{}

	// Exact match index (all types).
	serialized := serializeValue(val)
	byVal, ok := idx.exact[key]
	if !ok {
		byVal = make(map[string]map[string]struct{})
		idx.exact[key] = byVal
	}
	nodes, ok := byVal[serialized]
	if !ok {
		nodes = make(map[string]struct{})
		byVal[serialized] = nodes
	}
	nodes[nodeID] = struct{}{}

	// Substring index (string type only).
	if val.Type == graph.TypeString {
		byNode, ok := idx.strings[key]
		if !ok {
			byNode = make(map[string]string)
			idx.strings[key] = byNode
		}
		byNode[nodeID] = val.StringValue()
	}

	// Keyword index (string list type only).
	if val.Type == graph.TypeStringList {
		byKW, ok := idx.keywords[key]
		if !ok {
			byKW = make(map[string]map[string]struct{})
			idx.keywords[key] = byKW
		}
		for _, kw := range val.StringList() {
			nodes, ok := byKW[kw]
			if !ok {
				nodes = make(map[string]struct{})
				byKW[kw] = nodes
			}
			nodes[nodeID] = struct{}{}
		}
	}
}

// Remove removes a specific property value for a node from the index.
// Call this before updating a property (remove old value, then add new).
func (idx *MemoryPropertyIndex) Remove(nodeID, key string, val graph.Property) {
	// Exact match.
	serialized := serializeValue(val)
	if byVal, ok := idx.exact[key]; ok {
		if nodes, ok := byVal[serialized]; ok {
			delete(nodes, nodeID)
			if len(nodes) == 0 {
				delete(byVal, serialized)
			}
		}
		if len(byVal) == 0 {
			delete(idx.exact, key)
		}
	}

	// Substring index.
	if val.Type == graph.TypeString {
		if byNode, ok := idx.strings[key]; ok {
			delete(byNode, nodeID)
			if len(byNode) == 0 {
				delete(idx.strings, key)
			}
		}
	}

	// Keyword index.
	if val.Type == graph.TypeStringList {
		if byKW, ok := idx.keywords[key]; ok {
			for _, kw := range val.StringList() {
				if nodes, ok := byKW[kw]; ok {
					delete(nodes, nodeID)
					if len(nodes) == 0 {
						delete(byKW, kw)
					}
				}
			}
			if len(byKW) == 0 {
				delete(idx.keywords, key)
			}
		}
	}

	// Update reverse index.
	if keys, ok := idx.nodeKeys[nodeID]; ok {
		delete(keys, key)
		if len(keys) == 0 {
			delete(idx.nodeKeys, nodeID)
		}
	}
}

// RemoveNode removes all indexed properties for a node. Call this when
// a node is deleted. Requires the node's current properties to clean up.
func (idx *MemoryPropertyIndex) RemoveNode(nodeID string, props graph.Properties) {
	for key, val := range props {
		idx.Remove(nodeID, key, val)
	}
}

// Lookup returns all node IDs with an exact property match.
func (idx *MemoryPropertyIndex) Lookup(key string, val graph.Property) []string {
	serialized := serializeValue(val)
	byVal, ok := idx.exact[key]
	if !ok {
		return nil
	}
	nodes, ok := byVal[serialized]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(nodes))
	for id := range nodes {
		result = append(result, id)
	}
	return result
}

// Contains returns all node IDs where the string property for the given
// key contains the substring (case-sensitive).
func (idx *MemoryPropertyIndex) Contains(key, substring string) []string {
	byNode, ok := idx.strings[key]
	if !ok {
		return nil
	}
	var result []string
	for nodeID, val := range byNode {
		if strings.Contains(val, substring) {
			result = append(result, nodeID)
		}
	}
	return result
}

// ContainsFold returns all node IDs where the string property for the
// given key contains the substring (case-insensitive).
func (idx *MemoryPropertyIndex) ContainsFold(key, substring string) []string {
	byNode, ok := idx.strings[key]
	if !ok {
		return nil
	}
	lowerSub := strings.ToLower(substring)
	var result []string
	for nodeID, val := range byNode {
		if strings.Contains(strings.ToLower(val), lowerSub) {
			result = append(result, nodeID)
		}
	}
	return result
}

// LookupKeyword returns all node IDs where the StringList property for
// the given key contains the specified keyword (exact match).
func (idx *MemoryPropertyIndex) LookupKeyword(key, keyword string) []string {
	byKW, ok := idx.keywords[key]
	if !ok {
		return nil
	}
	nodes, ok := byKW[keyword]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(nodes))
	for id := range nodes {
		result = append(result, id)
	}
	return result
}

// NodesWithKey returns all node IDs that have the given key indexed.
func (idx *MemoryPropertyIndex) NodesWithKey(key string) map[string]struct{} {
	byVal, ok := idx.exact[key]
	if !ok {
		return nil
	}
	result := make(map[string]struct{})
	for _, nodes := range byVal {
		for id := range nodes {
			result[id] = struct{}{}
		}
	}
	return result
}

// KeywordCounts returns keyword -> count for all keywords under the
// given key. Used by curation to find concept candidates.
func (idx *MemoryPropertyIndex) KeywordCounts(key string) map[string]int {
	byKW, ok := idx.keywords[key]
	if !ok {
		return nil
	}
	counts := make(map[string]int, len(byKW))
	for kw, nodes := range byKW {
		counts[kw] = len(nodes)
	}
	return counts
}

// AddTx mirrors Add; the in-memory impl ignores tx.
func (idx *MemoryPropertyIndex) AddTx(_ *bolt.Tx, nodeID, key string, val graph.Property) {
	idx.Add(nodeID, key, val)
}

// RemoveTx mirrors Remove; the in-memory impl ignores tx.
func (idx *MemoryPropertyIndex) RemoveTx(_ *bolt.Tx, nodeID, key string, val graph.Property) {
	idx.Remove(nodeID, key, val)
}

// RemoveNodeTx mirrors RemoveNode; the in-memory impl ignores tx.
func (idx *MemoryPropertyIndex) RemoveNodeTx(_ *bolt.Tx, nodeID string, props graph.Properties) {
	idx.RemoveNode(nodeID, props)
}

// Count returns the total number of indexed entries across all keys.
func (idx *MemoryPropertyIndex) Count() int {
	total := 0
	for _, byVal := range idx.exact {
		for _, nodes := range byVal {
			total += len(nodes)
		}
	}
	return total
}

// serializeValue produces a deterministic string key for exact matching.
func serializeValue(val graph.Property) string {
	data, err := val.MarshalBinary()
	if err != nil {
		panic("index: failed to serialize property: " + err.Error())
	}
	return string(data)
}
