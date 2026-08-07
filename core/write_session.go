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

// SetContentProp updates a string property and refreshes the BM25
// index if the property is content_full. Mirrors Engine.SetContentProp.
func (ws *WriteSession) SetContentProp(nodeID, key, content string) {
	ws.indexes.setContentPropSession(ws, nodeID, key, content)
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
// self-deadlock inside the session's shared one.
func (ws *WriteSession) DeleteNode(id string) error {
	n, ok := ws.engine.graph.GetNode(id)
	if !ok {
		return fmt.Errorf("core: node %s: %w", id, graph.ErrNotFound)
	}
	ws.indexes.propIdx.RemoveNodeTx(ws.tx, id, n.Properties)
	ws.indexes.bm25Full.RemoveTx(ws.tx, ws.bm25, id)
	ws.indexes.vecIdx.Remove(id)
	if ws.indexes.secIdx != nil {
		ws.indexes.secIdx.RemoveNodeTx(ws.tx, id)
	}
	return ws.engine.graph.DeleteNodeTx(ws.tx, ws.edges, id)
}

// DeleteEdge removes an edge via the session's tx + edge batch.
func (ws *WriteSession) DeleteEdge(id string) error {
	return ws.engine.graph.DeleteEdgeTx(ws.tx, ws.edges, id)
}
