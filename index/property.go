package index

import (
	"sort"
	"strings"

	"github.com/brandonlattin/gramaton/graph"
)

// PropertyIndex supports exact match, range, and substring queries over
// node properties. It maintains internal data structures that must be
// kept in sync with the graph via Add/Remove calls.
//
// The index is not thread-safe. The server layer handles serialization.
type PropertyIndex struct {
	// Exact match: key → serialized value → set of node IDs.
	exact map[string]map[string]map[string]struct{}

	// Range queries: key → sorted slice of entries (ordered types only).
	sorted map[string][]rangeEntry

	// Substring search: key → node ID → string value (string properties only).
	strings map[string]map[string]string

	// Keyword index: key → keyword string → set of node IDs.
	// Built from StringList properties, indexing each element individually.
	keywords map[string]map[string]map[string]struct{}

	// Reverse index: node ID → set of keys indexed for that node.
	// Used by RemoveNode to clean up all entries for a deleted node.
	nodeKeys map[string]map[string]struct{}
}

// rangeEntry pairs a property value with the node it belongs to.
// Stored in sorted order within each key's slice.
type rangeEntry struct {
	Value  graph.Property
	NodeID string
}

// NewPropertyIndex creates an empty property index.
func NewPropertyIndex() *PropertyIndex {
	return &PropertyIndex{
		exact:    make(map[string]map[string]map[string]struct{}),
		sorted:   make(map[string][]rangeEntry),
		strings:  make(map[string]map[string]string),
		keywords: make(map[string]map[string]map[string]struct{}),
		nodeKeys: make(map[string]map[string]struct{}),
	}
}

// Add indexes a property value for a node. Call this when a node is
// created or a property is set.
func (idx *PropertyIndex) Add(nodeID, key string, val graph.Property) {
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

	// Range index (ordered types only).
	if isOrdered(val.Type) {
		entries := idx.sorted[key]
		pos := sort.Search(len(entries), func(i int) bool {
			c := entries[i].Value.Compare(val)
			if c == 0 {
				return entries[i].NodeID >= nodeID
			}
			return c > 0
		})
		// Insert at pos.
		entries = append(entries, rangeEntry{})
		copy(entries[pos+1:], entries[pos:])
		entries[pos] = rangeEntry{Value: val, NodeID: nodeID}
		idx.sorted[key] = entries
	}

	// Substring index (string type only).
	if val.Type == graph.TypeString {
		byNode, ok := idx.strings[key]
		if !ok {
			byNode = make(map[string]string)
			idx.strings[key] = byNode
		}
		byNode[nodeID] = val.String()
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
func (idx *PropertyIndex) Remove(nodeID, key string, val graph.Property) {
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

	// Range index.
	if isOrdered(val.Type) {
		entries := idx.sorted[key]
		for i, e := range entries {
			if e.NodeID == nodeID && e.Value.Equal(val) {
				idx.sorted[key] = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		if len(idx.sorted[key]) == 0 {
			delete(idx.sorted, key)
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
func (idx *PropertyIndex) RemoveNode(nodeID string, props graph.Properties) {
	for key, val := range props {
		idx.Remove(nodeID, key, val)
	}
}

// Lookup returns all node IDs with an exact property match.
func (idx *PropertyIndex) Lookup(key string, val graph.Property) []string {
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

// Range returns all node IDs where the property value is between min
// and max (inclusive). Only works for ordered types (String, Float64,
// Int64, Timestamp). Panics if min and max have different types.
func (idx *PropertyIndex) Range(key string, min, max graph.Property) []string {
	entries, ok := idx.sorted[key]
	if !ok {
		return nil
	}

	// Find lower bound: first entry >= min.
	lo := sort.Search(len(entries), func(i int) bool {
		return entries[i].Value.Compare(min) >= 0
	})

	// Collect entries from lo while <= max.
	seen := make(map[string]struct{})
	var result []string
	for i := lo; i < len(entries); i++ {
		if entries[i].Value.Compare(max) > 0 {
			break
		}
		id := entries[i].NodeID
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

// Contains returns all node IDs where the string property for the given
// key contains the substring (case-sensitive).
func (idx *PropertyIndex) Contains(key, substring string) []string {
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
func (idx *PropertyIndex) ContainsFold(key, substring string) []string {
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
func (idx *PropertyIndex) LookupKeyword(key, keyword string) []string {
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
func (idx *PropertyIndex) NodesWithKey(key string) map[string]struct{} {
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

// Count returns the total number of indexed entries across all keys.
func (idx *PropertyIndex) Count() int {
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

// isOrdered reports whether a property type supports comparison.
func isOrdered(t graph.PropertyType) bool {
	switch t {
	case graph.TypeString, graph.TypeFloat64, graph.TypeInt64, graph.TypeTimestamp:
		return true
	default:
		return false
	}
}
