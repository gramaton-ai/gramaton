package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/store"
)

// carveDanglingSampleCap bounds the per-response sample of dropped
// (dangling) edges. The full drop count is always reported grouped by
// edge_type; the sample is a debugging aid, not an exhaustive list, so
// a small fixed cap keeps the response bounded regardless of how many
// edges cross the selection boundary.
const carveDanglingSampleCap = 20

// CarveOutRequest selects a subset of the source store and materializes
// it into a brand-new destination store. The selection is the UNION of
// three seed types (at least one required):
//
//   - IDs: explicit record IDs, taken verbatim.
//   - a query: the same filter fields as ExportRequest, resolved
//     through the search machinery (memory records only -- sessions are
//     out of scope). Collections are invisible to search, so use
//     Collections for those.
//   - Collections: collection names or ids; pulls the collection node
//     (schema included, it rides as a property) and every member.
//
// The resolved seeds are expanded to a structural closure (chunks /
// sections of selected records, the collection node behind any selected
// member, and -- unless HeadsOnly -- superseded predecessors) before
// the faithful graph-level copy runs.
type CarveOutRequest struct {
	// Seeds (union; at least one required).
	IDs         []string `json:"ids,omitempty" jsonschema:"explicit record IDs to include verbatim"`
	Collections []string `json:"collections,omitempty" jsonschema:"collection names or ids; pulls the collection node, its schema, and all members"`

	// Query seed. Mirrors ExportRequest's filter subset. Resolved over
	// memory records only (sessions are never carried by a carve).
	Text            string            `json:"text,omitempty" jsonschema:"vector-similarity query"`
	Match           string            `json:"match,omitempty" jsonschema:"literal substring match across content fields"`
	Keywords        []string          `json:"keywords,omitempty" jsonschema:"keywords that must all be present"`
	Temporality     string            `json:"temporality,omitempty"`
	KnowledgeType   string            `json:"knowledge_type,omitempty"`
	EpistemicStatus string            `json:"epistemic_status,omitempty"`
	Resolution      string            `json:"resolution,omitempty"`
	Since           string            `json:"since,omitempty" jsonschema:"YYYY-MM-DD or RFC3339 lower bound on created_at"`
	Meta            map[string]string `json:"meta,omitempty"`

	// Destination. DestDataDir is required unless DryRun (an absolute
	// path to the new store's data directory, which must not yet
	// exist). DestName is an optional human-facing store name, validated
	// with the named-store alphabet and echoed back.
	DestName    string `json:"dest_name,omitempty"`
	DestDataDir string `json:"dest_data_dir,omitempty" jsonschema:"absolute path to the new store's data directory (must not exist)"`

	// Options.
	ReadOnly  bool `json:"read_only,omitempty" jsonschema:"freeze the destination read-only as the last step"`
	HeadsOnly bool `json:"heads_only,omitempty" jsonschema:"skip the supersedes closure (do not pull superseded predecessors)"`
	DryRun    bool `json:"dry_run,omitempty" jsonschema:"resolve and report the selection without creating or writing anything"`
}

// CarveDangling is one sampled edge that crosses the selection
// boundary (source in the carve, target outside) and was therefore
// dropped from the copy.
type CarveDangling struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"edge_type"`
}

// CarveOutResponse reports what the carve resolved to. On a DryRun the
// destination fields are empty and nothing was written; otherwise they
// name the store that was created.
type CarveOutResponse struct {
	DryRun bool `json:"dry_run"`

	DestName    string `json:"dest_name,omitempty"`
	DestDataDir string `json:"dest_data_dir,omitempty"`
	ReadOnly    bool   `json:"read_only"`

	// SeedCount is the size of the resolved seed set (before closure).
	// NodeCount is the size of the closed selection actually copied.
	SeedCount     int `json:"seed_count"`
	NodeCount     int `json:"node_count"`
	InteriorEdges int `json:"interior_edges"`

	// Dangling report: every edge from a copied node to a non-copied
	// node, counted by type, with a bounded sample.
	DroppedTotal   int             `json:"dropped_total"`
	DroppedByType  map[string]int  `json:"dropped_by_type,omitempty"`
	DanglingSample []CarveDangling `json:"dangling_sample,omitempty"`

	// EmbeddingDim is the vector dimension of the copied records (0 when
	// the selection carries no embeddings). The destination store is
	// created with this dimension so its vector index accepts the copied
	// vectors verbatim -- no re-embedding.
	EmbeddingDim int `json:"embedding_dim,omitempty"`
}

const CarveOutDescription = "Non-destructively copy a subset of the store (memory records + collections) into a brand-new store, optionally frozen read-only, for sharing. Seeds are the union of explicit record ids, a query, and named collections; structural children (chunks/sections), collection schemas, and superseded predecessors are pulled in automatically. Sessions are never carried. Reads the source only and writes a fresh destination; use dry_run to preview the selection without writing."

// carveNode is one gathered record: its id, a clone of every property
// (the FULL set -- embeddings, context_*, meta, author, valid_until,
// resolution -- not the export allowlist), the BM25 index text, and the
// embedding_full vector (nil when absent).
type carveNode struct {
	id      string
	props   graph.Properties
	content string
	vec     []float32
}

// carveEdge is one interior edge (both endpoints in the selection).
type carveEdge struct {
	src, dst string
	etype    string
	weight   float64
	props    graph.Properties
}

// CarveOut copies a selected subset of the source store into a new
// destination store via a faithful graph-level copy (IDs preserved,
// embeddings and structural edges carried), optionally freezing the
// result read-only.
//
// This is a READ on the source plus a WRITE to a fresh destination, so
// it is NOT gated behind the source's read-only flag: a frozen store is
// exactly the kind of store one carves a shareable subset out of.
//
// Lock discipline (three-phase): seeds + closure + gather all run under
// a single source RLock (pure in-memory graph traversal, no I/O -- the
// one I/O step, embedding the query text, happens off-lock before the
// RLock). The source lock is released before any destination I/O; the
// destination is populated on its own engine under its own write batch.
func (a *API) CarveOut(ctx context.Context, req CarveOutRequest) (CarveOutResponse, *APIError) {
	// A carve must name at least one seed source up front.
	if len(req.IDs) == 0 && len(req.Collections) == 0 && !carveHasQuery(req) {
		return CarveOutResponse{}, ErrMissing("at least one seed is required: ids, a query, or collections")
	}

	// Query seeds first, OFF the main lock: resolving a text query
	// embeds it (I/O). collectExportIDs handles the embed-off-lock +
	// brief RLock itself, so this must run before we take our own RLock.
	queryIDs, apiErr := a.carveResolveQuery(ctx, req)
	if apiErr != nil {
		return CarveOutResponse{}, apiErr
	}

	// Phase 1-3 under a single source RLock: resolve seeds -> closure ->
	// gather the faithful payload. No I/O inside this window.
	a.engine.RLock()
	g := a.engine.Graph()

	seeds := make(map[string]struct{})
	for _, id := range req.IDs {
		if _, ok := g.GetNode(id); !ok {
			a.engine.RUnlock()
			return CarveOutResponse{}, ErrNotFound("record not found: " + id)
		}
		seeds[id] = struct{}{}
	}
	for _, id := range queryIDs {
		if _, ok := g.GetNode(id); ok {
			seeds[id] = struct{}{}
		}
	}
	for _, c := range req.Collections {
		collID, ok := a.collectionByName(c)
		if !ok {
			// Fall back to treating c as a collection node id.
			if _, cerr := a.isCollection(c); cerr == nil {
				collID, ok = c, true
			}
		}
		if !ok {
			a.engine.RUnlock()
			return CarveOutResponse{}, ErrNotFound("collection not found: " + c)
		}
		seeds[collID] = struct{}{}
		for _, e := range a.collectionItemEdges(collID) {
			seeds[e.SourceID] = struct{}{}
		}
	}

	if len(seeds) == 0 {
		a.engine.RUnlock()
		return CarveOutResponse{}, ErrInvalid("carve selection is empty; provide ids, a query, or collections that resolve to records")
	}

	selection := carveClosure(g, seeds, req.HeadsOnly)

	// Gather the payload + dangling report under the same RLock.
	nodes := make([]carveNode, 0, len(selection))
	interior := make([]carveEdge, 0)
	droppedByType := make(map[string]int)
	droppedTotal := 0
	sample := make([]CarveDangling, 0, carveDanglingSampleCap)

	for id := range selection {
		n, ok := g.GetNode(id)
		if !ok {
			// Raced away between closure and gather; skip it (and its
			// edges are unreachable now anyway).
			continue
		}
		cn := carveNode{
			id:      id,
			props:   n.Properties.Clone(),
			content: core.RecordIndexText(n),
		}
		if v, ok := n.Properties.GetVector("embedding_full"); ok {
			cn.vec = v
		}
		nodes = append(nodes, cn)

		// Interior edges are copied; boundary-crossing edges are dropped
		// and reported. Walking outbound-only visits every interior edge
		// exactly once (each has its source in the selection) and yields
		// exactly the "from S to not-S" dangling set.
		for _, e := range g.EdgesFrom(id) {
			if _, in := selection[e.TargetID]; in {
				interior = append(interior, carveEdge{
					src:    e.SourceID,
					dst:    e.TargetID,
					etype:  e.Type,
					weight: e.Weight,
					props:  e.Properties.Clone(),
				})
				continue
			}
			droppedTotal++
			droppedByType[e.Type]++
			if len(sample) < carveDanglingSampleCap {
				sample = append(sample, CarveDangling{
					SourceID: e.SourceID,
					TargetID: e.TargetID,
					Type:     e.Type,
				})
			}
		}
	}

	// Source's declared dimension as a fallback for the (rare)
	// no-embeddings selection.
	srcCfgDim := a.engine.Config().Embedding.Dimension
	a.engine.RUnlock()

	// Embedding dimension check: every copied vector must share one
	// dimension. A mixed selection is a corrupt source, not something to
	// silently truncate.
	embedDim := 0
	for _, cn := range nodes {
		if cn.vec == nil {
			continue
		}
		if embedDim == 0 {
			embedDim = len(cn.vec)
			continue
		}
		if len(cn.vec) != embedDim {
			return CarveOutResponse{}, ErrInvalid(fmt.Sprintf(
				"source selection has inconsistent embedding dimensions (%d and %d); cannot carve",
				embedDim, len(cn.vec)))
		}
	}

	resp := CarveOutResponse{
		DryRun:         req.DryRun,
		ReadOnly:       req.ReadOnly && !req.DryRun,
		SeedCount:      len(seeds),
		NodeCount:      len(nodes),
		InteriorEdges:  len(interior),
		DroppedTotal:   droppedTotal,
		DroppedByType:  droppedByType,
		DanglingSample: sample,
		EmbeddingDim:   embedDim,
	}

	if req.DryRun {
		// Resolve + report only: no dir creation, no writes.
		return resp, nil
	}

	// Materialize. destDim pins the destination's vector index size so
	// the copied vectors are accepted verbatim (the mmap index silently
	// drops vectors whose length != its configured dimension).
	destDim := embedDim
	if destDim == 0 {
		destDim = srcCfgDim
	}
	if apiErr := a.carveMaterialize(req, nodes, interior, destDim); apiErr != nil {
		return CarveOutResponse{}, apiErr
	}

	resp.DestName = req.DestName
	resp.DestDataDir = req.DestDataDir
	return resp, nil
}

// carveMaterialize creates the destination store dir, opens it writable,
// copies the gathered nodes + interior edges in a single write batch,
// closes, and (when requested) freezes it read-only as the strict last
// step. Any failure removes the freshly-created store home so a failed
// carve leaves no trace.
func (a *API) carveMaterialize(req CarveOutRequest, nodes []carveNode, interior []carveEdge, destDim int) *APIError {
	if req.DestDataDir == "" {
		return ErrMissing("dest_data_dir is required unless dry_run is true")
	}
	if !filepath.IsAbs(req.DestDataDir) {
		return ErrInvalid("dest_data_dir must be an absolute path")
	}
	if req.DestName != "" {
		if err := store.ValidateName(req.DestName); err != nil {
			return ErrInvalid(err.Error())
		}
	}

	// Whole-home existence check (mirrors store.Attach): the store HOME
	// -- the parent of the data dir, which also holds config.yaml -- must
	// not already exist. Passing this check is what makes the wholesale
	// RemoveAll on failure safe: everything under the home was created
	// here.
	home := filepath.Dir(req.DestDataDir)
	if _, err := os.Stat(home); err == nil {
		return ErrConflict("destination store home already exists")
	} else if !os.IsNotExist(err) {
		a.log.Warn("carve: stat destination home", "component", "carveout", "err", err)
		return ErrInternal("failed to check destination")
	}

	fail := func(apiErr *APIError) *APIError {
		_ = os.RemoveAll(home)
		return apiErr
	}

	if err := os.MkdirAll(req.DestDataDir, 0o700); err != nil {
		a.log.Warn("carve: create destination dir", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to create destination directory"))
	}

	// Minimal destination config: pin data_dir so a global config can't
	// bleed through (see store.WriteDataDirConfig for that rationale),
	// disable the embedder (a shared read-only artifact never
	// re-embeds; keeping it off means no model load on open), and pin
	// the embedding dimension to the copied vectors' dimension so the
	// vec index rebuilds coherently.
	destCfg := config.Defaults()
	destCfg.DataDir = req.DestDataDir
	destCfg.Embedding.Provider = ""
	if destDim > 0 {
		destCfg.Embedding.Dimension = destDim
	}
	destCfg.LLM.Provider = ""
	if err := config.Save(destCfg, filepath.Join(home, "config.yaml")); err != nil {
		a.log.Warn("carve: write destination config", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to write destination config"))
	}

	// Open the destination WRITABLE (its STORE manifest is absent, so it
	// opens writable) and populate it.
	destEng, err := core.LoadEngine(home)
	if err != nil {
		a.log.Warn("carve: open destination engine", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to open destination store"))
	}

	originStore := a.storeName
	if originStore == "" {
		originStore = "(default)"
	}

	writeErr := destEng.WithWriteBatch("carve_out", func(ws *core.WriteSession) (bool, error) {
		// Pass 1: nodes. AddNodeWithID preserves each source ULID
		// (safe -- the destination is fresh), IndexNode populates every
		// index including the vector (from the copied embedding_full).
		for _, cn := range nodes {
			cn.props["origin_store"] = graph.StringProperty(originStore)
			ws.Graph().AddNodeWithID(cn.id, cn.props)
			ws.IndexNode(cn.id, cn.content, cn.vec)
		}
		// Pass 2: interior edges. Both endpoints exist (added in pass 1).
		// Written outbound-only during gather, so each edge is created
		// exactly once here.
		for _, ce := range interior {
			if _, err := ws.AddEdge(ce.src, ce.dst, ce.etype, ce.weight, ce.props); err != nil {
				return false, fmt.Errorf("carve edge %s->%s (%s): %w", ce.src, ce.dst, ce.etype, err)
			}
		}
		ws.AddAction(graph.CommitAction{Kind: graph.ActionCarveOut})
		return len(nodes) > 0, nil
	})
	if writeErr != nil {
		a.log.Warn("carve: populate destination", "component", "carveout", "err", writeErr)
		_ = destEng.Close()
		return fail(ErrInternal("failed to populate destination store"))
	}

	// Close flushes vectors + commits to disk. Must precede any freeze.
	if err := destEng.Close(); err != nil {
		a.log.Warn("carve: close destination engine", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to finalize destination store"))
	}

	// Freeze LAST: a store frozen before population would open read-only
	// and reject the very writes above. Owner is the source/creating
	// config's author identity.
	if req.ReadOnly {
		owner := a.engine.Config().Author.String()
		if err := core.FreezeStore(req.DestDataDir, owner); err != nil {
			a.log.Warn("carve: freeze destination", "component", "carveout", "err", err)
			return fail(ErrInternal("failed to freeze destination store"))
		}
	}

	return nil
}

// carveClosure expands the seed set to its structural + supersedes
// closure by fixpoint. Caller must hold at least a source RLock.
//
// Rules applied to every node X reached:
//   - inbound chunk_of/section_of (child -> X): pull the child, so a
//     selected record brings its chunks/sections.
//   - outbound chunk_of/section_of (X -> parent): pull the parent, so a
//     directly-selected chunk brings its record.
//   - outbound member_of (X -> collection): pull the collection (its
//     schema rides as a property), so a selected member brings its
//     governing collection.
//   - outbound supersedes (X -> predecessor): pull the predecessor
//     unless headsOnly, so a selected head brings its history.
//
// Session structure (topic_of / segment_of / extracted_as /
// continues_from and the session/topic/segment nodes they reach) is
// never followed: those edge types are absent from the switch, so a
// carve never carries session data.
func carveClosure(g *graph.Graph, seeds map[string]struct{}, headsOnly bool) map[string]struct{} {
	selection := make(map[string]struct{}, len(seeds))
	queue := make([]string, 0, len(seeds))
	add := func(id string) {
		if _, ok := selection[id]; ok {
			return
		}
		selection[id] = struct{}{}
		queue = append(queue, id)
	}
	for id := range seeds {
		add(id)
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]

		for _, e := range g.EdgesTo(x) {
			if e.Type == "chunk_of" || e.Type == "section_of" {
				add(e.SourceID)
			}
		}
		for _, e := range g.EdgesFrom(x) {
			switch e.Type {
			case "chunk_of", "section_of":
				add(e.TargetID)
			case "member_of":
				add(e.TargetID)
			case "supersedes":
				if !headsOnly {
					add(e.TargetID)
				}
			}
		}
	}
	return selection
}

// carveResolveQuery resolves the request's query seed to a list of
// record IDs, or returns nil when the request carries no query. Sessions
// are excluded (Store="memory") so a query can never seed a session
// segment. Reuses collectExportIDs, which embeds off-lock and takes its
// own brief RLock.
func (a *API) carveResolveQuery(ctx context.Context, req CarveOutRequest) ([]string, *APIError) {
	if !carveHasQuery(req) {
		return nil, nil
	}
	er := ExportRequest{
		Text:            req.Text,
		Match:           req.Match,
		Store:           "memory",
		Keywords:        req.Keywords,
		Temporality:     req.Temporality,
		KnowledgeType:   req.KnowledgeType,
		EpistemicStatus: req.EpistemicStatus,
		Resolution:      req.Resolution,
		Since:           req.Since,
		Meta:            req.Meta,
	}
	return a.collectExportIDs(ctx, er)
}

// carveHasQuery reports whether the request carries any query-seed
// filter (the caller-facing fields only -- Store is injected by
// carveResolveQuery, not user-supplied here).
func carveHasQuery(req CarveOutRequest) bool {
	return req.Text != "" ||
		req.Match != "" ||
		req.Temporality != "" ||
		req.KnowledgeType != "" ||
		req.EpistemicStatus != "" ||
		req.Resolution != "" ||
		req.Since != "" ||
		len(req.Keywords) > 0 ||
		len(req.Meta) > 0
}
