// Package core provides the shared engine. write_session.go defines
// WriteSession, the batched-write API used inside Engine.WithWriteBatch
// closures.
package core

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	bolt "go.etcd.io/bbolt"
)

// WriteSession owns a shared bbolt transaction and the companion
// caches for one batched write phase. All mutations inside a session
// land under ws.tx; BM25 and EdgeStore changes accumulate in ws.bm25 /
// ws.edges and flush at session end.
//
// Callers obtain a WriteSession from Engine.WithWriteBatch; non-
// batched callers continue to use Engine.SetProp/Graph.AddEdge/etc.
// directly (those open their own bbolt Update per call).
//
// The fields are unexported; the type exposes caller-facing methods
// that mirror the engine's non-batched mutation API. See D40.
type WriteSession struct {
	tx      *bolt.Tx
	bm25    *index.BM25Batch
	edges   *graph.EdgeBatch
	engine  *Engine
	indexes *indexSet
	// actions accumulates D3 structured action descriptors for the
	// batch. Engine.WithWriteBatch passes them to Save so the
	// resulting commit carries per-record intent, not just the free-
	// form Message. Callers accumulate via ws.AddAction within the
	// batch closure; nil/empty is fine (commit still filterable by
	// Message prefix).
	actions []graph.CommitAction
	// vecRemovals defers vector-index deletions until the shared tx
	// commits. The vector index is mmap-backed and its writes persist
	// regardless of the bbolt transaction's fate; removing eagerly
	// would make a rolled-back batch permanently drop records from
	// vector search (startup rebuild skips a non-empty vector
	// snapshot, so there is no self-healing path).
	vecRemovals []string
}

// AddAction records a D3 structured action for the current batch.
// Callers emit one per record-scoped change (capture, resolve,
// collection_add, curation-touch-record, etc.). Duplicate actions
// are stored verbatim -- consumers that want dedup should filter
// at read time. Thread-safe only under the engine write lock,
// which WithWriteBatch already holds while fn runs.
func (ws *WriteSession) AddAction(a graph.CommitAction) {
	ws.actions = append(ws.actions, a)
}

// Tx returns the underlying bbolt transaction. Exposed for low-level
// callers that need direct tx access (e.g. backup/import walking raw
// records). Most callers use the higher-level WriteSession methods.
func (ws *WriteSession) Tx() *bolt.Tx { return ws.tx }

// Graph returns the underlying graph for read-side access within the
// session. Read methods (GetNode, EdgesFrom, etc.) see the committed
// state plus in-memory changes made during this session.
func (ws *WriteSession) Graph() *graph.Graph { return ws.engine.graph }

// PropIdx returns the property index for callers that need to query
// by key during a write phase.
func (ws *WriteSession) PropIdx() index.PropertyIndex { return ws.indexes.propIdx }

// AddNode creates a new node in the graph. Does NOT populate the
// indexes -- call IndexNode or the appropriate per-index add once
// the node's full property set is assembled.
func (ws *WriteSession) AddNode(props graph.Properties) *graph.Node {
	return ws.engine.graph.AddNode(props)
}

// AddEdge creates a new edge in the graph via the session's tx +
// edge-batch cache.
func (ws *WriteSession) AddEdge(sourceID, targetID, edgeType string, weight float64, props graph.Properties) (*graph.Edge, error) {
	return ws.engine.graph.AddEdgeTx(ws.tx, ws.edges, sourceID, targetID, edgeType, weight, props)
}

// SetProp removes the prior value (if any) from the property index,
// writes the new value to the graph, and re-indexes -- all via the
// session's tx. Mirrors Engine.SetProp.
func (ws *WriteSession) SetProp(nodeID, key string, val graph.Property) {
	ws.indexes.setPropSession(ws, nodeID, key, val)
}

// SetContentProp updates a string property and, for lexical-document
// fields (content_full, content_short), re-derives the node's
// complete BM25 document. Mirrors Engine.SetContentProp.
func (ws *WriteSession) SetContentProp(nodeID, key, content string) {
	ws.indexes.setContentPropSession(ws, nodeID, key, content)
}

// AddVector writes (or overwrites in place) a node's entry in the
// vector index without touching BM25 or the property index. Used by
// chunking's parent-embedding replacement, where the property write
// goes through SetProp and only the vector posting needs syncing.
// The vector index is mmap-backed and persists outside the session's
// bbolt transaction; on the rare rolled-back batch, reembed heals the
// resulting vector/property divergence.
func (ws *WriteSession) AddVector(nodeID string, vec []float32) {
	ws.indexes.vecIdx.Add(nodeID, vec)
}

// IndexNode populates every index for a node via the session's tx +
// caches. Mirrors Engine.IndexNode.
func (ws *WriteSession) IndexNode(nodeID, content string, vec []float32) {
	if vec != nil {
		ws.engine.graph.SetNodeProperty(nodeID, "embedding_full", graph.VectorProperty(vec))
	}
	n, ok := ws.engine.graph.GetNode(nodeID)
	if !ok {
		return
	}
	ws.indexes.applyToNodeSession(ws, n, content, vec)
}

// DeleteNode hard-deletes a node: every index entry, the cascading
// edges, and the node itself, all via the session's tx + caches.
// Mirrors the plain-lock GC deletion pattern for batched callers --
// the non-Tx variants open their own bbolt transactions and would
// self-deadlock inside the session's shared one. The vector-index
// removal is deferred to the tx commit (see vecRemovals).
//
// Constraint: the cascade set comes from the COMMITTED adjacency
// lists. An edge added via ws.AddEdge earlier in the same session is
// invisible to it -- do not add an edge and then delete one of its
// endpoints within one batch, or the edge dangles.
func (ws *WriteSession) DeleteNode(id string) error {
	n, ok := ws.engine.graph.GetNode(id)
	if !ok {
		return fmt.Errorf("core: node %s: %w", id, graph.ErrNotFound)
	}
	// Collection membership caches are keyed by collection, not by
	// member: cascading the member_of edge alone would leak the
	// deleted ID in the collection's persistent member list forever.
	if ws.indexes.collCache != nil {
		for _, e := range ws.engine.graph.EdgesFrom(id) {
			if e.Type == "member_of" {
				ws.indexes.collCache.RemoveMemberTx(ws.tx, e.TargetID, id)
			}
		}
	}
	ws.indexes.propIdx.RemoveNodeTx(ws.tx, id, n.Properties)
	ws.indexes.bm25Full.RemoveTx(ws.tx, ws.bm25, id)
	// Vector-index and access-sidecar removals both persist outside
	// the shared bbolt tx, so they defer to the post-commit flush.
	ws.vecRemovals = append(ws.vecRemovals, id)
	if ws.indexes.secIdx != nil {
		ws.indexes.secIdx.RemoveNodeTx(ws.tx, id)
	}
	return ws.engine.graph.DeleteNodeTx(ws.tx, ws.edges, id)
}

// DeleteEdge removes an edge inside the session's shared write
// transaction. This is the MANDATORY in-batch path: the plain
// Graph.DeleteEdge opens its own bbolt update and would self-deadlock
// against the session's transaction. Call sites outside a batch use
// Graph.DeleteEdge directly, which performs identical bookkeeping.
func (ws *WriteSession) DeleteEdge(id string) error {
	return ws.engine.graph.DeleteEdgeTx(ws.tx, ws.edges, id)
}
