# P2-06 Execution Plan: `WriteSession` Pattern

Status: **All stages landed**. Stage 1 commit `2994c30`; Stages 2 + 3 in a single follow-up commit.

See [D40](design-decisions.md#d40-writesession-pattern-for-batched-index-writes-p2-06) for the architectural choice rationale.

Dev collection item: `01KPEDC410F0FAS2EN6FARXEWP`.

## Goal

Eliminate the stashed `batch *bolt.Tx` hazard on five bbolt-backed types (`BboltPropertyIndex`, `BboltBM25Index`, `BboltSecondaryIndex`, `BboltCollectionCache`, `BboltEdgeStore`) by threading the transaction and companion caches through an explicit `core.WriteSession` value.

## Stage 1: Hoist companion caches into value types (landed)

Commit `2994c30`. Pure internal restructure, no API change:

- `BboltBM25Index`'s loose `batchPostings` + `batchReverse` fields collapse into a single `*bm25Batch` pointer to a new value type (`bm25Batch` with `postings` + `reverse` fields).
- `BboltEdgeStore`'s loose `batchAdjOut` + `batchAdjIn` + `batchAdjTyp` fields collapse into a single `*edgeBatch` pointer to a new value type (`edgeBatch` with `adjOut` + `adjIn` + `adjTyp` fields).

## Stage 2: Tx-suffixed method variants + remove stashed state

Single commit. All interfaces and impls change together so intermediate state compiles.

### Interface changes

- `index.PropertyIndex`:
  - Add `AddTx(tx *bolt.Tx, nodeID, key string, val graph.Property)`.
  - Add `RemoveTx(tx *bolt.Tx, nodeID, key string, val graph.Property)`.
  - Add `RemoveNodeTx(tx *bolt.Tx, nodeID string, props graph.Properties)`.
  - Remove `Batch(fn func()) error` (only caller is `backup/import.go`, migrated in Stage 3).
- `index.BM25Index`:
  - Add `AddTx(tx *bolt.Tx, batch *BM25Batch, nodeID, text string)`.
  - Add `AddPreTokenizedTx(tx *bolt.Tx, batch *BM25Batch, nodeID string, termFreqs map[string]int, docLength int)`.
  - Add `RemoveTx(tx *bolt.Tx, batch *BM25Batch, nodeID string)`.
  - Remove `Batch(fn func()) error`.
- `graph.EdgeStore`:
  - Add `PutTx(tx *bolt.Tx, batch *EdgeBatch, e *Edge)`.
  - Add `DeleteTx(tx *bolt.Tx, batch *EdgeBatch, id string)`.

### Concrete type changes

- `BboltPropertyIndex`: remove `batch *bolt.Tx` field, `SetBatch`, `ClearBatch`, `update`, `view` helpers. Public `Add`/`Remove`/`RemoveNode` reimplement as a thin `db.Update` wrapper around the corresponding `*Tx` variant. Reads (`Lookup`, `Contains`, `ContainsFold`, `LookupKeyword`, `NodesWithKey`, `KeywordCounts`, `Count`) drop the `view` wrapper — each opens its own `db.View` inline. (`Range` was removed in P2-04 cleanup.)
- `BboltBM25Index`: remove `batch`, `batchSt` fields (hoisted type `BM25Batch` becomes exported, stays as a value type). Remove `SetBatch`, `ClearBatch`, `update`, `view`. Add `FlushBatchTx(tx *bolt.Tx, batch *BM25Batch)` for use by `indexSet.batch`. Internal helpers (`getPostings`, `setPostings`, `getReverseTerms`, `setReverseTerms`, `addToPostings`, `removeFromPostingsViaReverse`) grow a `batch *BM25Batch` parameter; nil `batch` disables caching. Public `Add`/`AddPreTokenized`/`Remove` become `db.Update` wrappers around the `*Tx` variants.
- `BboltSecondaryIndex`: remove `batch`, `SetBatch`, `ClearBatch`, `update`, `view`. Add `SetCreatedAtTx`, `SetLastAccessedTx`, `SetEdgeCountsTx`, `SetFieldExistsTx`, `ClearFieldExistsTx`, `RemoveNodeTx`. Public non-Tx methods become wrappers.
- `BboltCollectionCache`: remove `batch`, `SetBatch`, `ClearBatch`, `update`, `view`. Add `AddMemberTx`, `RemoveMemberTx`, `DeleteCollectionTx`. Public non-Tx methods become wrappers.
- `BboltEdgeStore`: remove `batch`, `batchSt` fields. Remove `SetBatch`, `flushBatch`, `pickBatchCache`. Export `EdgeBatch` + `NewEdgeBatch`. Add `PutTx`, `DeleteTx`, `FlushBatchTx` (parallel to the BM25 pattern).
- `graph.Graph`: add `AddEdgeTx(tx *bolt.Tx, batch *EdgeBatch, sourceID, targetID, edgeType string, weight float64, props Properties) (*Edge, error)`. Uses `EdgeStore.PutTx`. The existing `AddEdge` is unchanged — it still opens its own tx via the interface's `Put`.

### In-memory impls

- `MemoryPropertyIndex`, `MemoryBM25Index`, `MemoryEdgeStore` each gain stub `*Tx` variants that ignore the `tx` / `batch` arguments and delegate to the existing non-Tx methods.

### Rebuild path

- `core/indexes.go::rebuildIndexes` currently type-asserts `index.PropertyIndex` and `index.BM25Index` to a batch-setter interface (`SetBatch` + `ClearBatch`) to batch the rebuild walk. Rewrite to open one `db.Update`, call `AddTx`/`AddPreTokenizedTx` per node, no type assertion.

### Affected files

`index/property.go`, `index/bm25.go`, `index/bbolt_property.go`, `index/bbolt_bm25.go`, `index/bbolt_secondary.go`, `index/bbolt_collections.go`, `graph/edge_store.go`, `graph/bbolt_edges.go`, `graph/graph.go`, `core/indexes.go`.

## Stage 3: `WriteSession` + `WithWriteBatch` signature flip

Single commit, follows Stage 2.

### New type in core

```go
// WriteSession owns a shared bbolt transaction and the companion
// caches for one batched write phase. All mutations land under ws.tx;
// BM25 and EdgeStore changes accumulate in ws.bm25 / ws.edges and
// flush at session end.
type WriteSession struct {
    tx      *bolt.Tx
    bm25    *index.BM25Batch
    edges   *graph.EdgeBatch
    engine  *Engine
    indexes *indexSet
}

// Caller-facing mutation methods (match Engine's non-batched API):
func (ws *WriteSession) AddNode(props graph.Properties) *graph.Node
func (ws *WriteSession) AddEdge(sourceID, targetID, edgeType string, weight float64, props graph.Properties) (*graph.Edge, error)
func (ws *WriteSession) SetProp(nodeID, key string, val graph.Property)
func (ws *WriteSession) SetContentProp(nodeID, key, content string)
func (ws *WriteSession) IndexNode(nodeID, content string, vec []float32)
// Graph() returns the underlying graph for read-side access within the session.
func (ws *WriteSession) Graph() *graph.Graph
```

### `indexSet.batch` rewrite

```go
// batch opens a single bbolt Update, constructs the WriteSession,
// runs fn inside the tx, and flushes the BM25 + Edge caches before
// commit.
func (s *indexSet) batch(e *Engine, fn func(*WriteSession) error) error {
    return s.boltDB.Update(func(tx *bolt.Tx) error {
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
        return nil
    })
}
```

### `Engine.WithWriteBatch` signature change

```go
// Before (current):
func (e *Engine) WithWriteBatch(message string, fn func() (mutated bool, err error)) error

// After:
func (e *Engine) WithWriteBatch(message string, fn func(*WriteSession) (mutated bool, err error)) error
```

Body rewrites to: take `Lock`, call `indexes.batch(e, func(ws) ...)`, call `fn(ws)` inside, unwrap mutated/err, `Save`/`Unlock` on the existing timing code.

### Caller migrations

- `curation/observe.go::extractAndCreateObservations`: the closure passed to `WithWriteBatch` already accepts no arguments; it now accepts `*WriteSession`. Inside the closure, every `e.Graph().AddNode(props)`, `e.Graph().AddEdge(...)`, `e.IndexNode(...)` becomes `ws.AddNode(...)`, `ws.AddEdge(...)`, `ws.IndexNode(...)`. Graph reads (via `e.Graph().GetNode`) stay unchanged — `WriteSession.Graph()` returns the same underlying `*graph.Graph`.
- `curation/deterministic.go` write phase: same pattern. Replaces `e.SetProp`, `e.SetContentProp`, `e.Graph().AddEdge`, `e.PropIdx().Add`, `e.IndexNode` with `ws.SetProp`, `ws.SetContentProp`, `ws.AddEdge`, and a new `ws.AddProp` (if needed) / `ws.IndexNode`.
- `backup/import.go`: currently uses `e.PropIdx().Batch(fn func())` + `e.PropIdx().Add(...)` inside. Switch to `e.WithWriteBatch("backup: import", func(ws *WriteSession) (bool, error) { ... ws.IndexNode / ws.AddEdge ... })`.
- `index/bbolt_bm25_test.go::TestBboltBM25IndexBatch` (or equivalent): uses `idx.Batch(func())` — rewrite to `idx.Batch(func(tx *bolt.Tx) { idx.AddPreTokenizedTx(tx, nil, ...) })` after Stage 2, or drop and let the integration-level tests cover it.

### Test updates

- `core/engine_test.go::TestWithWriteBatchSaves`, `TestWithWriteBatchSkipsSaveOnNoMutations`, `TestWithWriteBatchPropagatesFnError`: fn closures accept `*WriteSession`, use `ws.AddNode` + `ws.IndexNode` instead of `eng.Graph().AddNode`/`eng.IndexNode`.

### Affected files

`core/write_session.go` (new), `core/engine.go`, `core/indexes.go`, `core/engine_test.go`, `curation/observe.go`, `curation/deterministic.go`, `backup/import.go`, `index/bbolt_bm25_test.go`.

## Validation

- `go build ./...` clean after Stage 2 and after Stage 3.
- `go vet ./...` clean.
- `go test ./...` clean (race on `core`, `curation`, `index`, `graph`, `server` packages).
- Manual: run `gramaton serve` on a test store, fire a curation cycle, verify the "write batch complete" log message still appears with sensible `batch_ms` and `save_ms`.

## Why this can't land in pieces

Stage 2 changes the interfaces; any intermediate state where the interface has the new methods but impls don't, or vice versa, doesn't compile. Stage 3 similarly must flip `WithWriteBatch`'s fn signature together with its two callers (`curation/observe.go` and `curation/deterministic.go`). So Stages 2 + 3 land in two commits each atomic, rather than five smaller commits that each break the build.

## Out of scope

- `Engine.SetProp`, `Engine.IndexNode`, `Engine.SetContentProp` signatures do NOT change. Non-batched callers (all 80+ call sites in `api/`, `server/`, `chunking/`, tests) continue to work. Only `WithWriteBatch` closure bodies change (~6 call sites).
- Fine-grained locking: this refactor removes the preconditions that block finer-grained locking, but does not itself change the locking model. That work comes later.
- The `*bbolt.Tx` leak into `graph.Graph.AddEdgeTx` is accepted as honest leakage (see D40). A neutral `txbatch` package abstraction is deferred unless a second storage backend materializes.
