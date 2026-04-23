package storage

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
)

// ProllyTree is a content-addressed sorted map backed by a prolly tree.
// Keys are sorted strings (node/edge IDs). Values are content hashes
// (hex strings). The tree is persistent and immutable -- mutations
// produce a new root.
//
// Leaf nodes contain sorted key-value entries. Internal nodes contain
// the first key and chunk hash of each child. Split boundaries are
// determined by a content hash (FNV-1a) over the key, producing
// content-defined chunks that share structure across similar trees.
type ProllyTree struct {
	store          *Store
	rootHash       string
	targetChunkSize int
	splitMask      uint32
}

// ProllyNode is a single node in the prolly tree. Serialized as JSON
// and stored as a content-addressed chunk.
type ProllyNode struct {
	Leaf     bool           `json:"leaf"`
	Entries  []ProllyEntry  `json:"entries"`
}

// ProllyEntry is a key-value pair in a leaf, or a key-pointer pair
// in an internal node.
type ProllyEntry struct {
	Key   string `json:"k"`
	Value string `json:"v"` // content hash (leaf) or child node hash (internal)
}

const (
	// maxTreeDepth limits recursion depth to prevent stack overflow
	// from corrupt or cyclic chunk data.
	maxTreeDepth = 32

	// maxNodeEntries limits the number of entries per tree node
	// during deserialization to prevent memory exhaustion.
	maxNodeEntries = 100_000

	// Default prolly tree parameters. Used when config values are
	// zero (e.g., in tests or when loading trees created by others).
	defaultTargetChunkSize = 64
	defaultSplitBits       = 6
)

// ProllyConfig holds tuning parameters for prolly tree construction.
type ProllyConfig struct {
	// TargetChunkSize is the target number of entries per leaf chunk.
	// Controls the granularity of structural sharing between similar
	// trees. Default: 64.
	TargetChunkSize int

	// SplitBits is the number of low bits of FNV-1a hash that must be
	// zero to trigger a chunk boundary. Average chunk size is 2^bits.
	// Default: 6 (average 64 entries per chunk).
	SplitBits int
}

// NewProllyTree creates an empty prolly tree with the given config.
func NewProllyTree(s *Store, cfg ProllyConfig) *ProllyTree {
	target := cfg.TargetChunkSize
	if target <= 0 {
		target = defaultTargetChunkSize
	}
	bits := cfg.SplitBits
	if bits <= 0 {
		bits = defaultSplitBits
	}
	return &ProllyTree{
		store:           s,
		targetChunkSize: target,
		splitMask:       (1 << bits) - 1,
	}
}

// LoadProllyTree loads a prolly tree from an existing root hash.
// Read-only operations (Get, AllEntries, Diff) do not need config
// since they traverse existing structure without creating new chunks.
func LoadProllyTree(s *Store, rootHash string) *ProllyTree {
	return &ProllyTree{
		store:           s,
		rootHash:        rootHash,
		targetChunkSize: defaultTargetChunkSize,
		splitMask:       (1 << defaultSplitBits) - 1,
	}
}

// RootHash returns the root hash, or empty string for an empty tree.
func (t *ProllyTree) RootHash() string {
	return t.rootHash
}

// SetConfig applies chunking parameters to a loaded tree. Required
// before calling Update() on a tree created via LoadProllyTree().
func (t *ProllyTree) SetConfig(cfg ProllyConfig) {
	if cfg.TargetChunkSize > 0 {
		t.targetChunkSize = cfg.TargetChunkSize
	}
	if cfg.SplitBits > 0 {
		t.splitMask = (1 << cfg.SplitBits) - 1
	}
}

// Build constructs a prolly tree from a sorted slice of key-value entries.
// This is more efficient than inserting one at a time.
func (t *ProllyTree) Build(entries []ProllyEntry) error {
	if len(entries) == 0 {
		t.rootHash = ""
		return nil
	}

	// Build leaf level: split entries into chunks at content-defined boundaries.
	leafHashes, leafFirstKeys, err := t.buildLevel(entries, true)
	if err != nil {
		return err
	}

	// Build internal levels until we have a single root.
	childHashes := leafHashes
	childFirstKeys := leafFirstKeys

	for len(childHashes) > 1 {
		// Create internal entries pointing to children.
		internalEntries := make([]ProllyEntry, len(childHashes))
		for i := range childHashes {
			internalEntries[i] = ProllyEntry{Key: childFirstKeys[i], Value: childHashes[i]}
		}

		childHashes, childFirstKeys, err = t.buildLevel(internalEntries, false)
		if err != nil {
			return err
		}
	}

	t.rootHash = childHashes[0]
	return nil
}

// buildLevel splits entries into content-defined chunks and writes them.
// Returns the hash and first key of each chunk.
func (t *ProllyTree) buildLevel(entries []ProllyEntry, leaf bool) ([]string, []string, error) {
	var hashes []string
	var firstKeys []string

	start := 0
	for i, e := range entries {
		// Check for split boundary after minimum chunk size.
		atBoundary := i > start && t.shouldSplit(e.Key) && (i-start) >= t.targetChunkSize/4
		atEnd := i == len(entries)-1

		if atBoundary || atEnd {
			end := i + 1
			chunk := ProllyNode{
				Leaf:    leaf,
				Entries: entries[start:end],
			}
			hash, err := t.writeNode(chunk)
			if err != nil {
				return nil, nil, err
			}
			hashes = append(hashes, hash)
			firstKeys = append(firstKeys, entries[start].Key)
			start = end
		}
	}

	return hashes, firstKeys, nil
}

// ProllyMutation describes a key-level mutation for incremental update.
type ProllyMutation struct {
	Key    string
	Value  string // new content hash; ignored if Delete is true
	Delete bool
}

// Update incrementally applies mutations to an existing prolly tree.
// Only touches chunks on the path from root to each affected leaf,
// giving O(K * depth) I/O where K is the number of mutations and
// depth is typically 3-4 for trees up to millions of entries.
//
// If the tree is empty, Update builds from the non-deleted mutations.
// Mutations need not be sorted; they are sorted internally.
func (t *ProllyTree) Update(mutations []ProllyMutation) error {
	if len(mutations) == 0 {
		return nil
	}

	sort.Slice(mutations, func(i, j int) bool {
		return mutations[i].Key < mutations[j].Key
	})

	if t.rootHash == "" {
		var entries []ProllyEntry
		for _, m := range mutations {
			if !m.Delete {
				entries = append(entries, ProllyEntry{Key: m.Key, Value: m.Value})
			}
		}
		return t.Build(entries)
	}

	resultEntries, err := t.applyMutations(t.rootHash, mutations, 0)
	if err != nil {
		return err
	}

	if len(resultEntries) == 0 {
		t.rootHash = ""
		return nil
	}

	// Single chunk result is the new root.
	if len(resultEntries) == 1 {
		t.rootHash = resultEntries[0].Value
		return nil
	}

	// Multiple chunks: build internal levels above until single root.
	hashes := make([]string, len(resultEntries))
	firstKeys := make([]string, len(resultEntries))
	for i, e := range resultEntries {
		hashes[i] = e.Value
		firstKeys[i] = e.Key
	}
	for len(hashes) > 1 {
		internalEntries := make([]ProllyEntry, len(hashes))
		for i := range hashes {
			internalEntries[i] = ProllyEntry{Key: firstKeys[i], Value: hashes[i]}
		}
		var err error
		hashes, firstKeys, err = t.buildLevel(internalEntries, false)
		if err != nil {
			return err
		}
	}
	t.rootHash = hashes[0]
	return nil
}

// applyMutations recursively applies sorted mutations to a subtree.
// Returns the new (firstKey, chunkHash) entries for the parent level.
// May return 0 (subtree emptied), 1 (unchanged or updated), or
// multiple entries (subtree split due to growth).
func (t *ProllyTree) applyMutations(nodeHash string, mutations []ProllyMutation, depth int) ([]ProllyEntry, error) {
	if depth > maxTreeDepth {
		return nil, fmt.Errorf("prolly: update depth exceeds maximum (%d)", maxTreeDepth)
	}

	node, err := t.readNode(nodeHash)
	if err != nil {
		return nil, err
	}

	if node.Leaf {
		return t.applyLeafMutations(node, mutations)
	}
	return t.applyInternalMutations(node, mutations, depth)
}

// applyLeafMutations merges mutations into a leaf's entries and
// re-chunks the result using content-defined boundaries.
func (t *ProllyTree) applyLeafMutations(node *ProllyNode, mutations []ProllyMutation) ([]ProllyEntry, error) {
	merged := mergeLeafEntries(node.Entries, mutations)
	if len(merged) == 0 {
		return nil, nil
	}
	hashes, firstKeys, err := t.buildLevel(merged, true)
	if err != nil {
		return nil, err
	}
	result := make([]ProllyEntry, len(hashes))
	for i := range hashes {
		result[i] = ProllyEntry{Key: firstKeys[i], Value: hashes[i]}
	}
	return result, nil
}

// applyInternalMutations routes mutations to the correct children,
// recursively updates them, and re-chunks the internal level.
func (t *ProllyTree) applyInternalMutations(node *ProllyNode, mutations []ProllyMutation, depth int) ([]ProllyEntry, error) {
	var newEntries []ProllyEntry
	mutIdx := 0

	for i, child := range node.Entries {
		// Key upper bound: the next child's first key, or unbounded.
		var upperBound string
		if i+1 < len(node.Entries) {
			upperBound = node.Entries[i+1].Key
		}

		// Collect mutations for this child's key range.
		start := mutIdx
		for mutIdx < len(mutations) {
			if upperBound != "" && mutations[mutIdx].Key >= upperBound {
				break
			}
			mutIdx++
		}
		childMuts := mutations[start:mutIdx]

		if len(childMuts) == 0 {
			newEntries = append(newEntries, child)
			continue
		}

		childResult, err := t.applyMutations(child.Value, childMuts, depth+1)
		if err != nil {
			return nil, err
		}
		newEntries = append(newEntries, childResult...)
	}

	if len(newEntries) == 0 {
		return nil, nil
	}

	// Re-chunk the internal level with updated children.
	hashes, firstKeys, err := t.buildLevel(newEntries, false)
	if err != nil {
		return nil, err
	}
	result := make([]ProllyEntry, len(hashes))
	for i := range hashes {
		result[i] = ProllyEntry{Key: firstKeys[i], Value: hashes[i]}
	}
	return result, nil
}

// mergeLeafEntries merges sorted mutations into sorted leaf entries.
// Inserts add new entries. Updates replace existing values. Deletes
// remove entries.
func mergeLeafEntries(entries []ProllyEntry, mutations []ProllyMutation) []ProllyEntry {
	result := make([]ProllyEntry, 0, len(entries)+len(mutations))
	ei, mi := 0, 0

	for ei < len(entries) && mi < len(mutations) {
		switch {
		case entries[ei].Key < mutations[mi].Key:
			result = append(result, entries[ei])
			ei++
		case entries[ei].Key > mutations[mi].Key:
			if !mutations[mi].Delete {
				result = append(result, ProllyEntry{Key: mutations[mi].Key, Value: mutations[mi].Value})
			}
			mi++
		default: // same key
			if !mutations[mi].Delete {
				result = append(result, ProllyEntry{Key: mutations[mi].Key, Value: mutations[mi].Value})
			}
			ei++
			mi++
		}
	}
	for ; ei < len(entries); ei++ {
		result = append(result, entries[ei])
	}
	for ; mi < len(mutations); mi++ {
		if !mutations[mi].Delete {
			result = append(result, ProllyEntry{Key: mutations[mi].Key, Value: mutations[mi].Value})
		}
	}
	return result
}

// Get looks up a value by key. Returns ("", false) if not found.
func (t *ProllyTree) Get(key string) (string, bool) {
	if t.rootHash == "" {
		return "", false
	}
	return t.get(t.rootHash, key, 0)
}

func (t *ProllyTree) get(nodeHash, key string, depth int) (string, bool) {
	if depth > maxTreeDepth {
		return "", false
	}
	node, err := t.readNode(nodeHash)
	if err != nil {
		return "", false
	}

	if node.Leaf {
		lo, hi := 0, len(node.Entries)
		for lo < hi {
			mid := (lo + hi) / 2
			if node.Entries[mid].Key < key {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo < len(node.Entries) && node.Entries[lo].Key == key {
			return node.Entries[lo].Value, true
		}
		return "", false
	}

	childIdx := len(node.Entries) - 1
	for i := len(node.Entries) - 1; i >= 0; i-- {
		if key >= node.Entries[i].Key {
			childIdx = i
			break
		}
	}
	return t.get(node.Entries[childIdx].Value, key, depth+1)
}

// AllEntries returns all key-value pairs in sorted order.
func (t *ProllyTree) AllEntries() ([]ProllyEntry, error) {
	if t.rootHash == "" {
		return nil, nil
	}
	return t.allEntries(t.rootHash, 0)
}

func (t *ProllyTree) allEntries(nodeHash string, depth int) ([]ProllyEntry, error) {
	if depth > maxTreeDepth {
		return nil, fmt.Errorf("prolly: tree depth exceeds maximum (%d)", maxTreeDepth)
	}
	node, err := t.readNode(nodeHash)
	if err != nil {
		return nil, err
	}

	if node.Leaf {
		return node.Entries, nil
	}

	var result []ProllyEntry
	for _, e := range node.Entries {
		children, err := t.allEntries(e.Value, depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, children...)
	}
	return result, nil
}

// Diff returns the entries that differ between two trees.
// Added: in other but not in t. Removed: in t but not in other.
//
// Skips entire subtrees at any level when their hashes match via a
// merge-style walk through internal-node child lists. Under the
// common "few changes since last commit" shape most internal
// boundaries stay aligned (content-defined chunking is stable
// around unchanged keys), so the walk reaches O(changes) total work.
// Falls back to allEntries materialisation for two pathological
// shapes: mixed internal/leaf depth (e.g. after heavily skewed
// rebalancing) and internal boundaries that don't overlap at the
// same key. Both are rare for neighbouring commits in a single-
// user store and stay correct even when they fire.
func (t *ProllyTree) Diff(other *ProllyTree) (added, removed []ProllyEntry, err error) {
	oldEntries, newEntries, err := t.diffNodes(t.rootHash, other.rootHash, 0)
	if err != nil {
		return nil, nil, err
	}

	oldMap := make(map[string]string, len(oldEntries))
	for _, e := range oldEntries {
		oldMap[e.Key] = e.Value
	}
	newMap := make(map[string]string, len(newEntries))
	for _, e := range newEntries {
		newMap[e.Key] = e.Value
	}

	for _, e := range newEntries {
		if oldVal, ok := oldMap[e.Key]; !ok || oldVal != e.Value {
			added = append(added, e)
		}
	}
	for _, e := range oldEntries {
		if newVal, ok := newMap[e.Key]; !ok || newVal != e.Value {
			removed = append(removed, e)
		}
	}

	return added, removed, nil
}

func (t *ProllyTree) diffNodes(oldHash, newHash string, depth int) ([]ProllyEntry, []ProllyEntry, error) {
	if depth > maxTreeDepth {
		return nil, nil, fmt.Errorf("prolly: diff depth exceeds maximum (%d)", maxTreeDepth)
	}

	// Identical subtrees -> no contribution, skip entire subtree.
	if oldHash == newHash {
		return nil, nil, nil
	}

	// One-sided: everything on the non-empty side is Added or Removed.
	if oldHash == "" {
		entries, err := LoadProllyTree(t.store, newHash).allEntries(newHash, depth+1)
		return nil, entries, err
	}
	if newHash == "" {
		entries, err := t.allEntries(oldHash, depth+1)
		return entries, nil, err
	}

	oldNode, err := t.readNode(oldHash)
	if err != nil {
		return nil, nil, err
	}
	newNode, err := t.readNode(newHash)
	if err != nil {
		return nil, nil, err
	}

	// Both leaves: caller (Diff) map-diffs to filter out unchanged
	// entries that happen to share a leaf between the two commits.
	if oldNode.Leaf && newNode.Leaf {
		return oldNode.Entries, newNode.Entries, nil
	}

	// Mixed depth (one side leaf, the other internal). Rare: happens
	// after heavily-skewed rebalancing. Correct fallback: materialise
	// both subtrees and let the caller map-diff. Cost is bounded by
	// whichever side is smaller, still cheaper than the old full-tree
	// fallback because only the overlapping subtree pays.
	if oldNode.Leaf != newNode.Leaf {
		oldEntries, err := t.allEntries(oldHash, depth+1)
		if err != nil {
			return nil, nil, err
		}
		newEntries, err := LoadProllyTree(t.store, newHash).allEntries(newHash, depth+1)
		if err != nil {
			return nil, nil, err
		}
		return oldEntries, newEntries, nil
	}

	// Both internal. Merge-walk the child lists; recurse only into
	// children whose first-key aligns but whose content hash differs.
	// Content-defined chunking keeps most boundaries stable across
	// neighbouring commits, so this is the fast path.
	return t.diffInternalChildren(oldNode.Entries, newNode.Entries, depth+1)
}

// diffInternalChildren walks two sorted internal-node child lists as
// a two-pointer merge. Returns the old/new entries contributed by
// subtrees whose hashes actually differ. Matching (Key, Value) pairs
// are skipped entirely -- their subtrees are known identical so no
// chunks are fetched. Misaligned keys (boundary shifts from
// rebalancing) fall back to materialising the unmatched subtree via
// allEntries; the caller's map-diff cancels out any duplicate keys
// that still match on the other side.
func (t *ProllyTree) diffInternalChildren(oldChildren, newChildren []ProllyEntry, depth int) ([]ProllyEntry, []ProllyEntry, error) {
	var oldAcc, newAcc []ProllyEntry
	i, j := 0, 0
	for i < len(oldChildren) && j < len(newChildren) {
		oldEntry, newEntry := oldChildren[i], newChildren[j]
		switch {
		case oldEntry.Key == newEntry.Key:
			// Aligned boundary. diffNodes short-circuits on equal
			// hashes (no reads), recurses into the pair otherwise.
			oldSub, newSub, err := t.diffNodes(oldEntry.Value, newEntry.Value, depth)
			if err != nil {
				return nil, nil, err
			}
			oldAcc = append(oldAcc, oldSub...)
			newAcc = append(newAcc, newSub...)
			i++
			j++
		case oldEntry.Key < newEntry.Key:
			// Boundary exists only on old side at this key. Subtree's
			// keys might overlap with new subtrees we haven't walked
			// yet; emit them and let the caller's map-diff reconcile.
			sub, err := t.allEntries(oldEntry.Value, depth)
			if err != nil {
				return nil, nil, err
			}
			oldAcc = append(oldAcc, sub...)
			i++
		default: // oldEntry.Key > newEntry.Key
			sub, err := LoadProllyTree(t.store, newEntry.Value).allEntries(newEntry.Value, depth)
			if err != nil {
				return nil, nil, err
			}
			newAcc = append(newAcc, sub...)
			j++
		}
	}
	// Drain the tail of whichever list still has children.
	for ; i < len(oldChildren); i++ {
		sub, err := t.allEntries(oldChildren[i].Value, depth)
		if err != nil {
			return nil, nil, err
		}
		oldAcc = append(oldAcc, sub...)
	}
	for ; j < len(newChildren); j++ {
		e := newChildren[j]
		sub, err := LoadProllyTree(t.store, e.Value).allEntries(e.Value, depth)
		if err != nil {
			return nil, nil, err
		}
		newAcc = append(newAcc, sub...)
	}
	return oldAcc, newAcc, nil
}

// writeNode serializes and stores a prolly node.
func (t *ProllyTree) writeNode(node ProllyNode) (string, error) {
	data, err := json.Marshal(node)
	if err != nil {
		return "", fmt.Errorf("prolly: marshal node: %w", err)
	}
	return t.store.Write(data)
}

// readNode loads and deserializes a prolly node.
func (t *ProllyTree) readNode(hash string) (*ProllyNode, error) {
	data, err := t.store.Read(hash)
	if err != nil {
		return nil, fmt.Errorf("prolly: read node %s: %w", hash, err)
	}
	var node ProllyNode
	if err := json.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("prolly: unmarshal node: %w", err)
	}
	if len(node.Entries) > maxNodeEntries {
		return nil, fmt.Errorf("prolly: node has %d entries, exceeds maximum %d", len(node.Entries), maxNodeEntries)
	}
	return &node, nil
}

// shouldSplit returns true if this key should trigger a chunk boundary.
// Uses FNV-1a hash for fast, deterministic boundary detection.
func (t *ProllyTree) shouldSplit(key string) bool {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()&t.splitMask == 0
}

// EntryCount returns the total number of key-value entries in the tree.
func (t *ProllyTree) EntryCount() (int, error) {
	if t.rootHash == "" {
		return 0, nil
	}
	return t.countEntries(t.rootHash, 0)
}

func (t *ProllyTree) countEntries(nodeHash string, depth int) (int, error) {
	if depth > maxTreeDepth {
		return 0, fmt.Errorf("prolly: count depth exceeds maximum (%d)", maxTreeDepth)
	}
	node, err := t.readNode(nodeHash)
	if err != nil {
		return 0, err
	}
	if node.Leaf {
		return len(node.Entries), nil
	}
	total := 0
	for _, e := range node.Entries {
		n, err := t.countEntries(e.Value, depth+1)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// Helpers for building sorted entry lists from maps.

// SortedEntries creates a sorted slice of entries from a map.
func SortedEntries(m map[string]string) []ProllyEntry {
	entries := make([]ProllyEntry, 0, len(m))
	for k, v := range m {
		entries = append(entries, ProllyEntry{Key: k, Value: v})
	}
	sortEntries(entries)
	return entries
}

func sortEntries(entries []ProllyEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
}

