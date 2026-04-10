package index

import (
	"container/heap"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// HNSWIndex is a Hierarchical Navigable Small World graph for
// approximate nearest neighbor search. Implements VectorIndex.
//
// At small scale (< HNSWThreshold vectors), a FlatIndex wrapper
// provides exact results. Above the threshold, HNSW gives O(log N)
// search with high recall.
type HNSWIndex struct {
	nodes    map[string]*hnswNode
	entryID  string // entry point node (highest layer)
	maxLayer int    // current maximum layer
	dim      int    // vector dimensions (set on first Add)

	// Tuning parameters.
	M              int     // max connections per layer (default 16)
	Mmax0          int     // max connections at layer 0 (default 2*M)
	EfConstruction int     // search width during build (default 200)
	EfSearch       int     // search width during query (default 100)
	ml             float64 // level multiplier: 1/ln(M)

	rng *rand.Rand
}

type hnswNode struct {
	id    string
	vec   []float32
	layer int        // max layer this node exists in
	conns [][]string // conns[l] = neighbor IDs at layer l
}

// Safety limits for deserialization to prevent OOM from malicious input.
const (
	maxNodes         = 10_000_000 // 10M nodes max
	maxDim           = 65_536     // 64K dimensions max (dim*4 fits in int)
	maxLayers        = 100        // HNSW layers (typical max is ~20 for 1M nodes)
	maxConnsPerLayer = 10_000     // upper bound on connections per node per layer
)

// distNode pairs a node ID with its distance (1 - cosine similarity)
// for use in search candidates.
type distNode struct {
	id   string
	dist float32
}

// NewHNSWIndex creates a new empty HNSW index with the given parameters.
// Use m=0, efConstruction=0, efSearch=0 for defaults (16, 200, 100).
func NewHNSWIndex(m, efConstruction, efSearch int) *HNSWIndex {
	if m <= 0 {
		m = 16
	}
	if efConstruction <= 0 {
		efConstruction = 200
	}
	if efSearch <= 0 {
		efSearch = 100
	}
	return &HNSWIndex{
		nodes:          make(map[string]*hnswNode),
		M:              m,
		Mmax0:          2 * m,
		EfConstruction: efConstruction,
		EfSearch:       efSearch,
		ml:             1.0 / math.Log(float64(m)),
		rng:            rand.New(rand.NewSource(42)),
	}
}

// Add inserts a vector for a node ID. If the node already exists,
// it is removed and re-inserted with the new vector.
func (h *HNSWIndex) Add(nodeID string, vec []float32) {
	if len(vec) == 0 {
		return
	}
	if h.dim == 0 {
		h.dim = len(vec)
	}

	// Remove existing entry if present.
	if _, exists := h.nodes[nodeID]; exists {
		h.Remove(nodeID)
	}

	cp := make([]float32, len(vec))
	copy(cp, vec)

	level := h.randomLevel()
	node := &hnswNode{
		id:    nodeID,
		vec:   cp,
		layer: level,
		conns: make([][]string, level+1),
	}

	// First node: set as entry point.
	if len(h.nodes) == 0 {
		h.nodes[nodeID] = node
		h.entryID = nodeID
		h.maxLayer = level
		return
	}

	h.nodes[nodeID] = node

	// Phase 1: traverse from top layer down to level+1 (greedy, ef=1).
	ep := []string{h.entryID}
	for l := h.maxLayer; l > level; l-- {
		nearest := h.searchLayer(cp, ep, 1, l)
		if len(nearest) > 0 {
			ep = []string{nearest[0].id}
		}
	}

	// Phase 2: search and connect at each layer from min(level, maxLayer) to 0.
	topInsert := level
	if topInsert > h.maxLayer {
		topInsert = h.maxLayer
	}
	for l := topInsert; l >= 0; l-- {
		neighbors := h.searchLayer(cp, ep, h.EfConstruction, l)

		mMax := h.M
		if l == 0 {
			mMax = h.Mmax0
		}
		selected := h.selectNeighbors(neighbors, mMax)

		// Connect new node to selected neighbors.
		ids := make([]string, len(selected))
		for i, s := range selected {
			ids[i] = s.id
		}
		node.conns[l] = ids

		// Connect neighbors back to new node, pruning if needed.
		for _, s := range selected {
			nb := h.nodes[s.id]
			if nb == nil || l >= len(nb.conns) {
				continue
			}
			nb.conns[l] = append(nb.conns[l], nodeID)
			limit := h.M
			if l == 0 {
				limit = h.Mmax0
			}
			if len(nb.conns[l]) > limit {
				h.pruneConnections(nb, l, limit)
			}
		}

		// Use found neighbors as entry points for next layer.
		ep = make([]string, len(neighbors))
		for i, n := range neighbors {
			ep[i] = n.id
		}
	}

	// Update entry point if new node is at a higher layer.
	if level > h.maxLayer {
		h.entryID = nodeID
		h.maxLayer = level
	}
}

// Remove deletes a node's vector and disconnects it from all neighbors.
func (h *HNSWIndex) Remove(nodeID string) {
	node, ok := h.nodes[nodeID]
	if !ok {
		return
	}

	// Disconnect from all neighbors at each layer.
	for l := 0; l < len(node.conns); l++ {
		for _, nbID := range node.conns[l] {
			nb := h.nodes[nbID]
			if nb == nil || l >= len(nb.conns) {
				continue
			}
			// Remove nodeID from neighbor's connections.
			filtered := nb.conns[l][:0]
			for _, cid := range nb.conns[l] {
				if cid != nodeID {
					filtered = append(filtered, cid)
				}
			}
			nb.conns[l] = filtered
		}
	}

	delete(h.nodes, nodeID)

	// If we removed the entry point, pick a new one.
	if h.entryID == nodeID {
		h.entryID = ""
		h.maxLayer = 0
		for id, n := range h.nodes {
			if n.layer > h.maxLayer || h.entryID == "" {
				h.entryID = id
				h.maxLayer = n.layer
			}
		}
	}
}

// Search returns the top-k nearest neighbors to the query vector.
// If candidates is non-nil and small, uses flat scan over candidates
// for exact results. Otherwise uses HNSW graph traversal.
func (h *HNSWIndex) Search(query []float32, k int, candidates map[string]struct{}) []SearchResult {
	if len(h.nodes) == 0 || k <= 0 {
		return nil
	}

	// For small candidate sets, flat scan is faster and exact.
	if candidates != nil && len(candidates) < 5000 {
		return h.flatSearch(query, k, candidates)
	}

	// HNSW search.
	ep := []string{h.entryID}

	// Traverse from top layer to layer 1 (greedy, ef=1).
	for l := h.maxLayer; l > 0; l-- {
		nearest := h.searchLayer(query, ep, 1, l)
		if len(nearest) > 0 {
			ep = []string{nearest[0].id}
		}
	}

	// Search layer 0 with full efSearch.
	ef := h.EfSearch
	if ef < k {
		ef = k
	}
	results := h.searchLayer(query, ep, ef, 0)

	// Filter by candidates if provided.
	if candidates != nil {
		filtered := results[:0]
		for _, r := range results {
			if _, ok := candidates[r.id]; ok {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	// Convert to SearchResult, take top k.
	if len(results) > k {
		results = results[:k]
	}
	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			NodeID:     r.id,
			Similarity: 1 - r.dist,
		}
	}
	return out
}

// Len returns the number of vectors in the index.
func (h *HNSWIndex) Len() int {
	return len(h.nodes)
}

// flatSearch does brute-force cosine similarity over a candidate set.
func (h *HNSWIndex) flatSearch(query []float32, k int, candidates map[string]struct{}) []SearchResult {
	var results []SearchResult
	for id := range candidates {
		n, ok := h.nodes[id]
		if !ok {
			continue
		}
		sim := CosineSimilarity(query, n.vec)
		results = append(results, SearchResult{NodeID: id, Similarity: sim})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})
	if k < len(results) {
		results = results[:k]
	}
	return results
}

// searchLayer performs a beam search on a single HNSW layer.
// Returns the ef closest nodes to the query, sorted by distance (ascending).
func (h *HNSWIndex) searchLayer(query []float32, entryIDs []string, ef int, layer int) []distNode {
	visited := make(map[string]struct{}, ef*2)
	candidates := &distHeap{} // min-heap: closest first
	results := &distHeap{}    // will be used as max-heap via negated distances

	for _, id := range entryIDs {
		n, ok := h.nodes[id]
		if !ok || layer >= len(n.conns) && layer > n.layer {
			continue
		}
		d := cosineDistance(query, n.vec)
		visited[id] = struct{}{}
		heap.Push(candidates, distNode{id: id, dist: d})
		heap.Push(results, distNode{id: id, dist: -d}) // negated for max-heap behavior
	}

	for candidates.Len() > 0 {
		closest := heap.Pop(candidates).(distNode)

		// If the closest candidate is farther than the farthest result, stop.
		if results.Len() >= ef {
			farthestDist := -(*results)[0].dist // un-negate
			if closest.dist > farthestDist {
				break
			}
		}

		// Explore neighbors of the closest candidate.
		cn, ok := h.nodes[closest.id]
		if !ok || layer >= len(cn.conns) {
			continue
		}
		for _, nbID := range cn.conns[layer] {
			if _, seen := visited[nbID]; seen {
				continue
			}
			visited[nbID] = struct{}{}

			nb, ok := h.nodes[nbID]
			if !ok {
				continue
			}
			d := cosineDistance(query, nb.vec)

			shouldAdd := results.Len() < ef
			if !shouldAdd {
				farthestDist := -(*results)[0].dist
				shouldAdd = d < farthestDist
			}

			if shouldAdd {
				heap.Push(candidates, distNode{id: nbID, dist: d})
				heap.Push(results, distNode{id: nbID, dist: -d})
				if results.Len() > ef {
					heap.Pop(results) // remove farthest (most negative = largest dist)
				}
			}
		}
	}

	// Extract results, convert back to positive distances, sort ascending.
	out := make([]distNode, results.Len())
	for i := len(out) - 1; i >= 0; i-- {
		r := heap.Pop(results).(distNode)
		out[i] = distNode{id: r.id, dist: -r.dist} // un-negate
	}
	return out
}

// selectNeighbors selects the m closest nodes from candidates.
func (h *HNSWIndex) selectNeighbors(candidates []distNode, m int) []distNode {
	if len(candidates) <= m {
		return candidates
	}
	// candidates are already sorted by distance ascending from searchLayer.
	return candidates[:m]
}

// pruneConnections keeps only the limit closest connections for a node
// at the given layer.
func (h *HNSWIndex) pruneConnections(node *hnswNode, layer, limit int) {
	conns := node.conns[layer]
	if len(conns) <= limit {
		return
	}

	// Score all connections by distance to the node.
	scored := make([]distNode, 0, len(conns))
	for _, cid := range conns {
		cn, ok := h.nodes[cid]
		if !ok {
			continue
		}
		d := cosineDistance(node.vec, cn.vec)
		scored = append(scored, distNode{id: cid, dist: d})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].dist < scored[j].dist
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	pruned := make([]string, len(scored))
	for i, s := range scored {
		pruned[i] = s.id
	}
	node.conns[layer] = pruned
}

// randomLevel generates a random layer for a new node using the
// exponential distribution: floor(-ln(uniform) * mL).
func (h *HNSWIndex) randomLevel() int {
	return int(-math.Log(h.rng.Float64()) * h.ml)
}

// cosineDistance returns 1 - cosine_similarity. Lower is closer.
func cosineDistance(a, b []float32) float32 {
	return 1 - CosineSimilarity(a, b)
}

// --- Heap implementation for distNode ---

type distHeap []distNode

func (h distHeap) Len() int            { return len(h) }
func (h distHeap) Less(i, j int) bool  { return h[i].dist < h[j].dist }
func (h distHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *distHeap) Push(x interface{}) { *h = append(*h, x.(distNode)) }
func (h *distHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// --- Serialization ---
//
// Binary format (little-endian):
//
//   header:
//     magic          [4]byte  "HNSW"
//     version        uint16   1
//     M              uint16
//     Mmax0          uint16
//     efConstruction uint16
//     efSearch       uint16
//     dim            uint32
//     maxLayer       uint16
//     entryID_len    uint16
//     entryID        []byte
//     numNodes       uint32
//
//   per node (repeated numNodes times):
//     id_len    uint16
//     id        []byte
//     layer     uint16
//     vec       [dim]float32
//     per layer l=0..layer:
//       numConns  uint16
//       per conn:
//         conn_id_len  uint16
//         conn_id      []byte

// MarshalBinary serializes the HNSW index to a binary format.
func (h *HNSWIndex) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 0, 1024+len(h.nodes)*256)

	// Header.
	buf = append(buf, 'H', 'N', 'S', 'W')
	buf = binary.LittleEndian.AppendUint16(buf, 1) // version
	buf = binary.LittleEndian.AppendUint16(buf, uint16(h.M))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(h.Mmax0))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(h.EfConstruction))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(h.EfSearch))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.dim))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(h.maxLayer))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(h.entryID)))
	buf = append(buf, h.entryID...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(h.nodes)))

	// Nodes sorted by ID for deterministic output.
	ids := make([]string, 0, len(h.nodes))
	for id := range h.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		node := h.nodes[id]
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(id)))
		buf = append(buf, id...)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(node.layer))

		// Vector.
		for _, v := range node.vec {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
			buf = append(buf, b[:]...)
		}

		// Connections per layer.
		for l := 0; l <= node.layer; l++ {
			var conns []string
			if l < len(node.conns) {
				conns = node.conns[l]
			}
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(conns)))
			for _, cid := range conns {
				buf = binary.LittleEndian.AppendUint16(buf, uint16(len(cid)))
				buf = append(buf, cid...)
			}
		}
	}

	return buf, nil
}

// UnmarshalBinary restores an HNSW index from binary data.
func (h *HNSWIndex) UnmarshalBinary(data []byte) error {
	if len(data) < 24 {
		return fmt.Errorf("hnsw: data too short")
	}
	if string(data[:4]) != "HNSW" {
		return fmt.Errorf("hnsw: invalid magic")
	}
	pos := 4
	version := binary.LittleEndian.Uint16(data[pos : pos+2])
	pos += 2
	if version != 1 {
		return fmt.Errorf("hnsw: unsupported version %d", version)
	}

	h.M = int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if h.M < 2 {
		return fmt.Errorf("hnsw: M must be >= 2, got %d", h.M)
	}
	h.Mmax0 = int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	h.EfConstruction = int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	h.EfSearch = int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	h.dim = int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4
	if h.dim > maxDim {
		return fmt.Errorf("hnsw: dim %d exceeds maximum %d", h.dim, maxDim)
	}
	h.maxLayer = int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if h.maxLayer > maxLayers {
		return fmt.Errorf("hnsw: maxLayer %d exceeds maximum %d", h.maxLayer, maxLayers)
	}
	h.ml = 1.0 / math.Log(float64(h.M))
	if h.rng == nil {
		h.rng = rand.New(rand.NewSource(42))
	}

	entryIDLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+entryIDLen > len(data) {
		return fmt.Errorf("hnsw: truncated entry ID")
	}
	h.entryID = string(data[pos : pos+entryIDLen])
	pos += entryIDLen

	numNodes := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4
	if numNodes > maxNodes {
		return fmt.Errorf("hnsw: numNodes %d exceeds maximum %d", numNodes, maxNodes)
	}

	h.nodes = make(map[string]*hnswNode, numNodes)

	for i := 0; i < numNodes; i++ {
		if pos+2 > len(data) {
			return fmt.Errorf("hnsw: truncated at node %d", i)
		}
		idLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+idLen > len(data) {
			return fmt.Errorf("hnsw: truncated node ID at %d", i)
		}
		id := string(data[pos : pos+idLen])
		pos += idLen

		if pos+2 > len(data) {
			return fmt.Errorf("hnsw: truncated layer at node %d", i)
		}
		layer := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if layer > maxLayers {
			return fmt.Errorf("hnsw: node %d layer %d exceeds maximum %d", i, layer, maxLayers)
		}

		// Vector. Use safe multiplication to prevent overflow.
		vecSize := h.dim * 4 // safe: dim <= maxDim (65536), so dim*4 <= 262144
		if pos+vecSize > len(data) {
			return fmt.Errorf("hnsw: truncated vector at node %d", i)
		}
		vec := make([]float32, h.dim)
		for j := 0; j < h.dim; j++ {
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(data[pos : pos+4]))
			pos += 4
		}

		// Connections.
		conns := make([][]string, layer+1)
		for l := 0; l <= layer; l++ {
			if pos+2 > len(data) {
				return fmt.Errorf("hnsw: truncated conns at node %d layer %d", i, l)
			}
			numConns := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
			pos += 2
			if numConns > maxConnsPerLayer {
				return fmt.Errorf("hnsw: node %d layer %d has %d connections, max %d", i, l, numConns, maxConnsPerLayer)
			}
			conns[l] = make([]string, numConns)
			for c := 0; c < numConns; c++ {
				if pos+2 > len(data) {
					return fmt.Errorf("hnsw: truncated conn ID at node %d layer %d conn %d", i, l, c)
				}
				cidLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
				pos += 2
				if pos+cidLen > len(data) {
					return fmt.Errorf("hnsw: truncated conn data at node %d layer %d conn %d", i, l, c)
				}
				conns[l][c] = string(data[pos : pos+cidLen])
				pos += cidLen
			}
		}

		h.nodes[id] = &hnswNode{
			id:    id,
			vec:   vec,
			layer: layer,
			conns: conns,
		}
	}

	return nil
}
