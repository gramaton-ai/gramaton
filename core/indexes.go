package core

import (
	"fmt"
	"log/slog"
	"path/filepath"

	bolt "go.etcd.io/bbolt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// indexSet bundles the engine's indexes and the bbolt database that
// backs four of them. Grouping these has two distinct payoffs.
//
// First, applyToNode consolidates the cross-index update pattern that
// was previously spread across IndexNode + ad-hoc loops in the
// chunking pipeline. Each fresh node touches up to five indexes and
// it was easy to forget one when adding a new index or a new
// node-creation path; routing all those updates through a single
// helper makes the bug shape ("forgot to update an index") impossible
// rather than merely caught-by-tests.
//
// Second, the four bbolt-backed indexes (propIdx, bm25Full, secIdx,
// edgeStore) share one bbolt DB. Opening separate write transactions
// against the same DB deadlocks. batch coordinates a single shared
// transaction so callers don't have to know which index touches which
// bucket.
//
// All methods on indexSet assume the caller already holds the
// appropriate engine lock; they do no locking of their own.
type indexSet struct {
	propIdx   index.PropertyIndex
	vecIdx    index.VectorIndex
	bm25Full  index.BM25Index
	secIdx    *index.BboltSecondaryIndex
	collCache *index.BboltCollectionCache
	edgeStore *graph.BboltEdgeStore
	tsIndex   *graph.TSIndex
	boltDB    *bolt.DB
}

// newIndexSet creates the bbolt-backed indexes and the edge store.
// The vector index is left unset; the caller injects one via
// WithVectorIndex or calls openDefaultVecIdx after option processing.
// This split mirrors how the original LoadEngine flow let tests
// substitute an in-memory FlatIndex without paying for mmap setup.
//
// boltDB is retained as a reference for batch transactions; ownership
// (and Close) stays with the engine.
func newIndexSet(boltDB *bolt.DB, cfg config.Config) (*indexSet, error) {
	propIdx, err := index.NewBboltPropertyIndex(boltDB, index.DefaultIndexedFields)
	if err != nil {
		return nil, fmt.Errorf("create bbolt property index: %w", err)
	}
	edgeStore, err := graph.NewBboltEdgeStore(boltDB, graph.DefaultEdgeCacheCapacity)
	if err != nil {
		return nil, fmt.Errorf("create bbolt edge store: %w", err)
	}
	bm25Full, err := index.NewBboltBM25Index(boltDB, cfg.Search.BM25K1, cfg.Search.BM25B)
	if err != nil {
		return nil, fmt.Errorf("create bbolt BM25 index: %w", err)
	}
	secIdx, err := index.NewBboltSecondaryIndex(boltDB)
	if err != nil {
		return nil, fmt.Errorf("create secondary index: %w", err)
	}
	collCache, err := index.NewBboltCollectionCache(boltDB)
	if err != nil {
		return nil, fmt.Errorf("create collection cache: %w", err)
	}
	tsIndex, err := graph.NewTSIndex(boltDB)
	if err != nil {
		return nil, fmt.Errorf("create timestamp index: %w", err)
	}
	return &indexSet{
		propIdx:   propIdx,
		bm25Full:  bm25Full,
		secIdx:    secIdx,
		collCache: collCache,
		edgeStore: edgeStore,
		tsIndex:   tsIndex,
		boltDB:    boltDB,
	}, nil
}

// openDefaultVecIdx opens the on-disk mmap'd flat vector index using
// the embedding dimension from cfg. Skipped when WithVectorIndex
// already injected one. The returned cleanup closes the mmap file.
func (s *indexSet) openDefaultVecIdx(cfg config.Config, noSync bool) (func(), error) {
	if s.vecIdx != nil {
		return nil, nil
	}
	vecDim := cfg.Embedding.Dimension
	if vecDim <= 0 {
		vecDim = 384 // default dimension for bge-small-en-v1.5 (current default embedding model)
	}
	vecPath := filepath.Join(cfg.DataDir, "vec.flat")
	mmapVec, err := index.NewMmapFlatIndex(vecPath, vecDim)
	if err != nil {
		return nil, fmt.Errorf("open vector index: %w", err)
	}
	mmapVec.SetNoSync(noSync)
	s.vecIdx = mmapVec
	return func() { mmapVec.Close() }, nil
}

// applyToNode populates every index for a node that has just been
// added to the graph (or whose properties were just mutated). This
// is the single consolidator -- prefer it over hand-writing the
// per-index calls so future indexes are picked up automatically.
//
// Caller responsibilities:
//   - The node must already exist in the graph with all properties
//     set (including embedding_full when vec is non-nil; see IndexNode
//     for the canonical caller that handles this).
//   - content is the BM25-indexed text. Pass "" to skip BM25.
//   - vec is the embedding to register in the vector index. Pass nil
//     to skip the vector index. The vector itself does not need to be
//     present in n.Properties for vector-index registration; this is
//     intentional, mirroring the historical contract.
func (s *indexSet) applyToNode(n *graph.Node, content string, vec []float32) {
	for k, v := range n.Properties {
		s.propIdx.Add(n.ID, k, v)
		if s.secIdx != nil {
			s.secIdx.SetFieldExists(k, n.ID)
		}
	}
	// Concepts are derived summaries-of-many-records: they stay out
	// of the primary retrieval indexes (BM25 + vector) so they never
	// compete with their member records for top-N slots. The opt-in
	// include_concepts path retrieves them via the concept-side
	// cosine scan over their embedding_full property instead.
	concept := graph.IsConcept(n.Properties)
	if content != "" && !concept {
		s.bm25Full.Add(n.ID, content)
	}
	if vec != nil && !concept {
		s.vecIdx.Add(n.ID, vec)
	}
	if s.secIdx != nil {
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			s.secIdx.SetCreatedAt(n.ID, ca)
		}
		if la, ok := n.Properties.GetTimestamp("last_accessed"); ok {
			s.secIdx.SetLastAccessed(n.ID, la)
		}
	}
}

// setProp removes the prior value (if any) from the property index,
// writes the new value to the graph, and re-indexes. Maintains the
// secondary indexes (field existence and the last_accessed time
// index). The graph mutation lives here rather than at the call site
// because the index Remove must run before the graph mutation
// overwrites the old value -- splitting graph and index work would
// invite ordering bugs.
func (s *indexSet) setProp(g *graph.Graph, nodeID, key string, val graph.Property) {
	if n, ok := g.GetNode(nodeID); ok {
		if old, ok := n.Properties[key]; ok {
			s.propIdx.Remove(nodeID, key, old)
		}
	}
	g.SetNodeProperty(nodeID, key, val)
	s.propIdx.Add(nodeID, key, val)
	if s.secIdx != nil {
		s.secIdx.SetFieldExists(key, nodeID)
		if key == "last_accessed" {
			if t := val.Timestamp(); !t.IsZero() {
				s.secIdx.SetLastAccessed(nodeID, t)
			}
		}
	}
}

// setContentProp updates a string property and refreshes the BM25
// index when the property is content_full. D12 collapsed BM25 to a
// single layer over content_full only, so other content fields skip
// the BM25 update.
func (s *indexSet) setContentProp(g *graph.Graph, nodeID, key, content string) {
	s.setProp(g, nodeID, key, graph.StringProperty(content))
	if key == "content_full" {
		s.bm25Full.Remove(nodeID)
		// A concept's synthesis rewrite must not re-enter BM25 (the
		// Remove above also clears any entry a pre-exclusion store
		// left behind).
		if n, ok := g.GetNode(nodeID); ok && graph.IsConcept(n.Properties) {
			return
		}
		s.bm25Full.Add(nodeID, content)
	}
}

// applyToNodeSession is applyToNode via a WriteSession (uses the
// session's tx + BM25 batch cache).
func (s *indexSet) applyToNodeSession(ws *WriteSession, n *graph.Node, content string, vec []float32) {
	for k, v := range n.Properties {
		s.propIdx.AddTx(ws.tx, n.ID, k, v)
		if s.secIdx != nil {
			s.secIdx.SetFieldExistsTx(ws.tx, k, n.ID)
		}
	}
	// Same concept exclusion as applyToNode: derived nodes stay out
	// of BM25 + vector.
	concept := graph.IsConcept(n.Properties)
	if content != "" && !concept {
		s.bm25Full.AddTx(ws.tx, ws.bm25, n.ID, content)
	}
	if vec != nil && !concept {
		s.vecIdx.Add(n.ID, vec)
	}
	if s.secIdx != nil {
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			s.secIdx.SetCreatedAtTx(ws.tx, n.ID, ca)
		}
		if la, ok := n.Properties.GetTimestamp("last_accessed"); ok {
			s.secIdx.SetLastAccessedTx(ws.tx, n.ID, la)
		}
	}
}

// setPropSession is setProp via a WriteSession.
func (s *indexSet) setPropSession(ws *WriteSession, nodeID, key string, val graph.Property) {
	g := ws.engine.graph
	if n, ok := g.GetNode(nodeID); ok {
		if old, ok := n.Properties[key]; ok {
			s.propIdx.RemoveTx(ws.tx, nodeID, key, old)
		}
	}
	g.SetNodeProperty(nodeID, key, val)
	s.propIdx.AddTx(ws.tx, nodeID, key, val)
	if s.secIdx != nil {
		s.secIdx.SetFieldExistsTx(ws.tx, key, nodeID)
		if key == "last_accessed" {
			if t := val.Timestamp(); !t.IsZero() {
				s.secIdx.SetLastAccessedTx(ws.tx, nodeID, t)
			}
		}
	}
}

// setContentPropSession is setContentProp via a WriteSession,
// including its concept exclusion: the two paths write the same
// index, so a gate on one alone would let batched callers reinstate
// what the non-batched path refuses.
func (s *indexSet) setContentPropSession(ws *WriteSession, nodeID, key, content string) {
	s.setPropSession(ws, nodeID, key, graph.StringProperty(content))
	if key == "content_full" {
		s.bm25Full.RemoveTx(ws.tx, ws.bm25, nodeID)
		// A concept's synthesis rewrite must not re-enter BM25 (the
		// Remove above also clears any entry a pre-exclusion store
		// left behind).
		if n, ok := ws.engine.graph.GetNode(nodeID); ok && graph.IsConcept(n.Properties) {
			return
		}
		s.bm25Full.AddTx(ws.tx, ws.bm25, nodeID, content)
	}
}

// batch opens a single bbolt write transaction, constructs a
// WriteSession carrying the tx plus the BM25 + Edge in-batch caches,
// runs fn, and flushes the caches before commit. Use for bulk node
// creation paths (session extraction, import, curation) where per-
// node fsync overhead would dominate runtime.
//
// A non-nil return means the transaction was rolled back -- index
// writes inside fn did not persist. Callers must check.
func (s *indexSet) batch(e *Engine, fn func(*WriteSession) error) error {
	var vecRemovals []string
	err := s.boltDB.Update(func(tx *bolt.Tx) error {
		ws := &WriteSession{
			tx:      tx,
			bm25:    index.NewBM25Batch(),
			edges:   graph.NewEdgeBatch(),
			engine:  e,
			indexes: s,
		}
		if err := fn(ws); err != nil {
			return err
		}
		if bm, ok := s.bm25Full.(*index.BboltBM25Index); ok {
			bm.FlushBatchTx(tx, ws.bm25)
		}
		if s.edgeStore != nil {
			s.edgeStore.FlushBatchTx(tx, ws.edges)
		}
		vecRemovals = ws.vecRemovals
		return nil
	})
	if err != nil {
		return err
	}
	// Vector and access-sidecar removals apply only after the tx
	// committed: both persist independently of bbolt, so an eager
	// removal inside a rolled-back batch would permanently drop the
	// record from vector search (rebuild skips non-empty snapshots)
	// or regress its access bookkeeping.
	for _, id := range vecRemovals {
		s.vecIdx.Remove(id)
		if e.accessIdx != nil {
			e.accessIdx.Remove(id)
		}
	}
	return nil
}

// batchMetaBucket holds cross-batch bookkeeping for the shared index
// DB. pendingBatchHeadKey records the graph HEAD as of the last
// mutating write batch: the batch's bbolt commit and the graph
// commit that follows it are two separate fsyncs, and a crash
// between them leaves the index DB ahead of the graph. A stored
// head equal to the loaded HEAD at boot means exactly that -- the
// batch landed, its graph commit did not.
var (
	batchMetaBucket     = []byte("batch_meta")
	pendingBatchHeadKey = []byte("pending_batch_head")
)

// stampPendingBatchHeadTx records the pre-batch HEAD inside the
// batch's own transaction. No clearing pass exists on purpose: the
// graph commit that follows a healthy batch moves HEAD away from
// the stamp, which is the same signal at zero extra fsyncs.
func (s *indexSet) stampPendingBatchHeadTx(tx *bolt.Tx, head string) error {
	b, err := tx.CreateBucketIfNotExists(batchMetaBucket)
	if err != nil {
		return err
	}
	return b.Put(pendingBatchHeadKey, []byte(head))
}

// pendingBatchHead returns the recorded pre-batch HEAD and whether a
// stamp exists at all. The two must stay distinct: the pre-batch
// HEAD of an empty store is legitimately "", so key presence -- not
// a non-empty value -- is the ran-at-least-once signal.
func (s *indexSet) pendingBatchHead() (string, bool) {
	var h string
	var found bool
	_ = s.boltDB.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(batchMetaBucket); b != nil {
			if raw := b.Get(pendingBatchHeadKey); raw != nil {
				h = string(raw)
				found = true
			}
		}
		return nil
	})
	return h, found
}

// rebuildPrimaryIfMissing populates the primary indexes (prop, bm25,
// vec) from the graph at startup. bm25 and vec are skipped when their
// persisted snapshot is non-empty; propIdx always rebuilds.
//
// Why propIdx always rebuilds: Count() > 0 is a partial-load trap.
// When a new property is added to DefaultIndexedFields, or when an
// older record predates an indexing change, the bbolt-backed index
// has data for some keys but not others. Count() > 0 short-circuits
// the rebuild, leaving the missing keys un-indexed forever -- so
// queries like Missing[X] silently return wrong results. Always
// rebuilding fixes this. AddTx is idempotent (addToIDSet dedups), so
// the re-walk is cheap when the index is already complete.
func (s *indexSet) rebuildPrimaryIfMissing(g *graph.Graph) {
	bm25FullLoaded := s.bm25Full.Len() > 0
	vecLoaded := s.vecIdx.Len() > 0
	rebuildIndexes(s.boltDB, g, s.propIdx, s.vecIdx, s.bm25Full, bm25FullLoaded, vecLoaded)
}

// rebuildAll force-rebuilds every index (primary + secondary) from
// the graph. Used by Engine.RebuildAllIndexes after operations that
// may have mutated graph state out from under the index (branch
// checkout/merge, restore). Caller must hold the engine write lock.
func (s *indexSet) rebuildAll(g *graph.Graph) {
	rebuildIndexes(s.boltDB, g, s.propIdx, s.vecIdx, s.bm25Full, false, false)
	if s.secIdx != nil {
		s.rebuildSecondary(g)
	}
}

// rebuildSecondary populates the secondary index from graph state.
// Walks the node iterator twice -- once for per-node properties and
// timestamps, once for edge counts -- because the second pass needs
// each node ID to query EdgesTo/EdgesFrom.
func (s *indexSet) rebuildSecondary(g *graph.Graph) {
	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			s.secIdx.SetCreatedAt(n.ID, ca)
		}
		if la, ok := n.Properties.GetTimestamp("last_accessed"); ok {
			s.secIdx.SetLastAccessed(n.ID, la)
		}
		for k := range n.Properties {
			s.secIdx.SetFieldExists(k, n.ID)
		}
	}
	it2 := g.NodeIterator()
	defer it2.Close()
	for it2.Next() {
		id := it2.Node().ID
		inEdges := g.EdgesTo(id)
		outEdges := g.EdgesFrom(id)
		inCount, outCount := 0, 0
		for _, edge := range inEdges {
			if !graph.IsStructuralEdge(edge.Type) {
				inCount++
			}
		}
		for _, edge := range outEdges {
			if !graph.IsStructuralEdge(edge.Type) {
				outCount++
			}
		}
		s.secIdx.SetEdgeCounts(id, inCount, outCount)
	}
}

// close releases the vector index when it implements Close. The
// shared bbolt database is owned by the engine and closed separately.
func (s *indexSet) close() error {
	if c, ok := s.vecIdx.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// rebuildIndexes batches the rebuild across the bbolt-backed indexes
// in a single write transaction. propIdx and bm25Full share the same
// bbolt DB; opening separate write transactions for each would
// deadlock. propIdx is always rebuilt (see rebuildPrimaryIfMissing
// for the partial-load rationale); bm25 and vec are skipped when
// their persisted data is non-empty.
//
// Uses the Tx-suffixed Add variants directly, threading tx through the
// bbolt-backed impls; in-memory impls accept and ignore the tx. This
// replaces the old SetBatch type-assertion dance.
func rebuildIndexes(db *bolt.DB, g graph.NodeReader, propIdx index.PropertyIndex, vecIdx index.VectorIndex, bm25Full index.BM25Index, bm25FullLoaded, vecLoaded bool) {
	slog.Info("rebuilding indexes from graph",
		"component", "engine",
		"bm25_loaded", bm25FullLoaded,
		"vec_loaded", vecLoaded)

	doRebuild := func(tx *bolt.Tx, bm25Batch *index.BM25Batch) {
		it := g.NodeIterator()
		defer it.Close()
		for it.Next() {
			n := it.Node()
			for k, v := range n.Properties {
				propIdx.AddTx(tx, n.ID, k, v)
			}
			// Concepts stay out of BM25 + vector (derived-layer
			// contract); rebuild is also how legacy stores shed their
			// pre-exclusion concept entries.
			if graph.IsConcept(n.Properties) {
				continue
			}
			if !vecLoaded {
				for _, embKey := range []string{"embedding_full", "embedding_medium", "embedding_short", "embedding_keywords"} {
					if v, ok := n.Properties.GetVector(embKey); ok {
						vecIdx.Add(n.ID, v)
						break
					}
				}
			}
			if !bm25FullLoaded {
				if text := RecordIndexText(n); text != "" {
					bm25Full.AddTx(tx, bm25Batch, n.ID, text)
				}
			}
		}
	}

	// Always need a tx because propIdx always rebuilds; in-memory-only
	// test setups skip the outer Update.
	if db != nil {
		db.Update(func(tx *bolt.Tx) error {
			bm25Batch := index.NewBM25Batch()
			doRebuild(tx, bm25Batch)
			if bm, ok := bm25Full.(*index.BboltBM25Index); ok {
				bm.FlushBatchTx(tx, bm25Batch)
			}
			return nil
		})
		return
	}
	doRebuild(nil, nil)
}
