package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/backup"
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

// carveSelection is the seed + query + closure specification shared by
// the create carve (CarveOut) and the top-up (CarveAdd). Both entry
// points resolve the SAME union of seeds -- explicit ids, a query, and
// named collections -- and expand the same structural + supersedes
// closure; only what they do with the gathered payload differs. Holding
// the selection in one internal struct lets carveGather serve both
// without either public request type having to embed the other.
type carveSelection struct {
	IDs         []string
	Collections []string

	Text            string
	Match           string
	Keywords        []string
	Temporality     string
	KnowledgeType   string
	EpistemicStatus string
	Resolution      string
	Since           string
	Meta            map[string]string

	HeadsOnly bool
}

// selection projects a CarveOutRequest onto the shared carveSelection.
func (req CarveOutRequest) selection() carveSelection {
	return carveSelection{
		IDs:             req.IDs,
		Collections:     req.Collections,
		Text:            req.Text,
		Match:           req.Match,
		Keywords:        req.Keywords,
		Temporality:     req.Temporality,
		KnowledgeType:   req.KnowledgeType,
		EpistemicStatus: req.EpistemicStatus,
		Resolution:      req.Resolution,
		Since:           req.Since,
		Meta:            req.Meta,
		HeadsOnly:       req.HeadsOnly,
	}
}

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

// carveGathered is the faithful payload produced under a single source
// RLock: the resolved + closed node set (full properties, index text, and
// embedding vector) plus EVERY outbound edge from those nodes, left
// UNPARTITIONED. Partitioning into interior vs dangling is deferred to
// carvePartitionEdges because the "present" set differs by caller: create
// partitions against the gathered set alone (a fresh destination holds
// nothing else), while the top-up partitions against the destination's
// existing nodes UNION the gathered set (so a missed record's edge to an
// already-present record reconnects instead of dangling).
type carveGathered struct {
	seedCount int
	nodes     []carveNode
	gathered  map[string]struct{}
	edges     []carveEdge

	// embedDim is the shared dimension of the gathered vectors (0 when the
	// selection carries no embeddings). srcCfgDim is the source's declared
	// embedding dimension, a fallback for the no-embeddings case.
	embedDim  int
	srcCfgDim int
}

// carveGather runs the READ side of a carve -- resolve seeds, expand the
// closure, and gather the faithful node + edge payload -- shared by
// CarveOut (create) and CarveAdd (top-up). Neither writes anything here.
//
// This is a READ on the source, so it is NOT gated behind the source's
// read-only flag: a frozen store is exactly the kind of store one carves
// (or tops up from) a shareable subset out of.
//
// Lock discipline (three-phase): the one I/O step -- embedding a text
// query -- happens off-lock inside carveResolveQuery; seeds + closure +
// gather then run under a SINGLE source RLock with no intervening unlock
// or I/O; the lock is released before the caller touches any destination.
func (a *API) carveGather(ctx context.Context, sel carveSelection) (*carveGathered, *APIError) {
	// A carve must name at least one seed source up front.
	if len(sel.IDs) == 0 && len(sel.Collections) == 0 && !carveHasQuery(sel) {
		return nil, ErrMissing("at least one seed is required: ids, a query, or collections")
	}

	// Query seeds first, OFF the main lock: resolving a text query
	// embeds it (I/O). collectExportIDs handles the embed-off-lock +
	// brief RLock itself, so this must run before we take our own RLock.
	queryIDs, apiErr := a.carveResolveQuery(ctx, sel)
	if apiErr != nil {
		return nil, apiErr
	}

	// Phase 1-3 under a single source RLock: resolve seeds -> closure ->
	// gather the faithful payload. No I/O inside this window.
	a.engine.RLock()
	g := a.engine.Graph()

	seeds := make(map[string]struct{})
	for _, id := range sel.IDs {
		if _, ok := g.GetNode(id); !ok {
			a.engine.RUnlock()
			return nil, ErrNotFound("record not found: " + id)
		}
		seeds[id] = struct{}{}
	}
	for _, id := range queryIDs {
		if _, ok := g.GetNode(id); ok {
			seeds[id] = struct{}{}
		}
	}
	for _, c := range sel.Collections {
		collID, ok := a.collectionByName(c)
		if !ok {
			// Fall back to treating c as a collection node id.
			if _, cerr := a.isCollection(c); cerr == nil {
				collID, ok = c, true
			}
		}
		if !ok {
			a.engine.RUnlock()
			return nil, ErrNotFound("collection not found: " + c)
		}
		seeds[collID] = struct{}{}
		for _, e := range a.collectionItemEdges(collID) {
			seeds[e.SourceID] = struct{}{}
		}
	}

	if len(seeds) == 0 {
		a.engine.RUnlock()
		return nil, ErrInvalid("carve selection is empty; provide ids, a query, or collections that resolve to records")
	}

	selection := carveClosure(g, seeds, sel.HeadsOnly)

	// Pass 1: materialize every selection id that resolves to a node,
	// recording the gathered id set.
	nodes := make([]carveNode, 0, len(selection))
	gathered := make(map[string]struct{}, len(selection))
	for id := range selection {
		n, ok := g.GetNode(id)
		if !ok {
			// The single source RLock spans resolve -> closure -> gather
			// with no intervening unlock or I/O, so no concurrent delete is
			// possible: a missing node here can only be a pre-existing
			// corrupt/phantom selection id (a closure target with no
			// backing node). Skip it -- and because it never enters
			// `gathered`, any edge that points at it is reported as
			// dangling by carvePartitionEdges rather than being
			// mis-classified interior (which would abort the destination
			// AddEdge on a missing endpoint and fail the whole carve).
			continue
		}
		cn := carveNode{
			id:      id,
			props:   n.Properties.Clone(),
			content: graph.RecordIndexText(n),
		}
		if v, ok := n.Properties.GetVector("embedding_full"); ok {
			cn.vec = v
		}
		nodes = append(nodes, cn)
		gathered[id] = struct{}{}
	}

	// Pass 2: gather EVERY outbound edge from a gathered node, left
	// unpartitioned. Walking outbound-only over the gathered set visits
	// each interior edge exactly once (its source is in the gathered set)
	// and captures every boundary-crossing candidate for the caller to
	// classify against its own "present" set.
	edges := make([]carveEdge, 0)
	for id := range gathered {
		for _, e := range g.EdgesFrom(id) {
			edges = append(edges, carveEdge{
				src:    e.SourceID,
				dst:    e.TargetID,
				etype:  e.Type,
				weight: e.Weight,
				props:  e.Properties.Clone(),
			})
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
			return nil, ErrInvalid(fmt.Sprintf(
				"source selection has inconsistent embedding dimensions (%d and %d); cannot carve",
				embedDim, len(cn.vec)))
		}
	}

	return &carveGathered{
		seedCount: len(seeds),
		nodes:     nodes,
		gathered:  gathered,
		edges:     edges,
		embedDim:  embedDim,
		srcCfgDim: srcCfgDim,
	}, nil
}

// carvePartition is the interior/dangling split of a gathered edge set
// against a "present" node set.
type carvePartition struct {
	interior      []carveEdge
	droppedTotal  int
	droppedByType map[string]int
	sample        []CarveDangling
}

// carvePartitionEdges splits gathered edges into interior (target present)
// and dangling (target absent) against a present-node set, building the
// grouped drop count and a bounded sample. Pure and side-effect free so
// create (present = the gathered set) and the top-up (present =
// destination nodes UNION the gathered set) share one classifier.
func carvePartitionEdges(edges []carveEdge, present map[string]struct{}) carvePartition {
	p := carvePartition{
		droppedByType: make(map[string]int),
		sample:        make([]CarveDangling, 0, carveDanglingSampleCap),
	}
	for _, e := range edges {
		if _, in := present[e.dst]; in {
			p.interior = append(p.interior, e)
			continue
		}
		p.droppedTotal++
		p.droppedByType[e.etype]++
		if len(p.sample) < carveDanglingSampleCap {
			p.sample = append(p.sample, CarveDangling{
				SourceID: e.src,
				TargetID: e.dst,
				Type:     e.etype,
			})
		}
	}
	return p
}

// CarveOut copies a selected subset of the source store into a new
// destination store via a faithful graph-level copy (IDs preserved,
// embeddings and structural edges carried), optionally freezing the
// result read-only.
//
// The read side (resolve + closure + gather) is carveGather; the write
// side materializes a BRAND-NEW destination on its own engine. A fresh
// destination holds nothing, so an edge is interior iff BOTH endpoints
// are in the gathered selection.
func (a *API) CarveOut(ctx context.Context, req CarveOutRequest) (CarveOutResponse, *APIError) {
	g, apiErr := a.carveGather(ctx, req.selection())
	if apiErr != nil {
		return CarveOutResponse{}, apiErr
	}

	part := carvePartitionEdges(g.edges, g.gathered)

	resp := CarveOutResponse{
		DryRun:         req.DryRun,
		ReadOnly:       req.ReadOnly && !req.DryRun,
		SeedCount:      g.seedCount,
		NodeCount:      len(g.nodes),
		InteriorEdges:  len(part.interior),
		DroppedTotal:   part.droppedTotal,
		DroppedByType:  part.droppedByType,
		DanglingSample: part.sample,
		EmbeddingDim:   g.embedDim,
	}

	if req.DryRun {
		// Resolve + report only: no dir creation, no writes.
		return resp, nil
	}

	// Materialize. destDim pins the destination's vector index size so
	// the copied vectors are accepted verbatim (the mmap index silently
	// drops vectors whose length != its configured dimension).
	destDim := g.embedDim
	if destDim == 0 {
		destDim = g.srcCfgDim
	}
	if apiErr := a.carveMaterialize(req, g.nodes, part.interior, destDim); apiErr != nil {
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

	// Destination config. Pin data_dir so a global config can't bleed
	// through (see store.WriteDataDirConfig for that rationale) and
	// INHERIT the source's embedding config (provider, model, ...) so the
	// carved store has a working query-time embedder: a recipient can
	// semantically search the shared store, not just probe it with raw
	// vectors. The default embedder is the bundled pure-Go `bert` (no API
	// key). Pin the embedding DIMENSION to the copied vectors' actual
	// dimension so the vec index rebuilds coherently and accepts the
	// copied vectors verbatim (in production the source-provider dim ==
	// copied-vector dim; the pin is a safety belt).
	//
	// The LLM stays DISABLED: a carved store runs no curation, and leaving
	// it off avoids writing the source's LLM credentials into the dest, so
	// we start from Defaults() rather than inheriting a.engine.Config().LLM.
	//
	// Secrets are then stripped from the on-disk config (backup.StripAPIKeys,
	// the same allowlist sanitizer backups use): the dest config.yaml is a
	// local artifact that ships with the shared store and must NEVER carry
	// a credential -- correct even when the source uses an API embedder.
	destCfg := config.Defaults()
	destCfg.DataDir = req.DestDataDir
	destCfg.Embedding = a.engine.Config().Embedding
	if destDim > 0 {
		destCfg.Embedding.Dimension = destDim
	}
	destCfg.LLM.Provider = ""
	cfgPath := filepath.Join(home, "config.yaml")
	if err := config.Save(destCfg, cfgPath); err != nil {
		a.log.Warn("carve: write destination config", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to write destination config"))
	}
	// Strip any inherited credential before the config can be shared.
	// Reusing StripAPIKeys (an allowlist, not a blocklist) means a future
	// secret-bearing embedding field is stripped too rather than leaking.
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		a.log.Warn("carve: reread destination config", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to sanitize destination config"))
	}
	sanitized, err := backup.StripAPIKeys(raw)
	if err != nil {
		a.log.Warn("carve: sanitize destination config", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to sanitize destination config"))
	}
	if err := os.WriteFile(cfgPath, sanitized, 0o600); err != nil {
		a.log.Warn("carve: rewrite destination config", "component", "carveout", "err", err)
		return fail(ErrInternal("failed to sanitize destination config"))
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

// carveResolveQuery resolves the selection's query seed to a list of
// record IDs, or returns nil when the selection carries no query.
// Sessions are excluded (Store="memory") so a query can never seed a
// session segment. Reuses collectExportIDs, which embeds off-lock and
// takes its own brief RLock.
func (a *API) carveResolveQuery(ctx context.Context, sel carveSelection) ([]string, *APIError) {
	if !carveHasQuery(sel) {
		return nil, nil
	}
	er := ExportRequest{
		Text:            sel.Text,
		Match:           sel.Match,
		Store:           "memory",
		Keywords:        sel.Keywords,
		Temporality:     sel.Temporality,
		KnowledgeType:   sel.KnowledgeType,
		EpistemicStatus: sel.EpistemicStatus,
		Resolution:      sel.Resolution,
		Since:           sel.Since,
		Meta:            sel.Meta,
	}
	return a.collectExportIDs(ctx, er)
}

// carveHasQuery reports whether the selection carries any query-seed
// filter (the caller-facing fields only -- Store is injected by
// carveResolveQuery, not user-supplied here).
func carveHasQuery(sel carveSelection) bool {
	return sel.Text != "" ||
		sel.Match != "" ||
		sel.Temporality != "" ||
		sel.KnowledgeType != "" ||
		sel.EpistemicStatus != "" ||
		sel.Resolution != "" ||
		sel.Since != "" ||
		len(sel.Keywords) > 0 ||
		len(sel.Meta) > 0
}
