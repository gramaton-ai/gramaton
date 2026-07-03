package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/store"
)

// CarveAddRequest tops up an EXISTING destination store with more of the
// source store's subset. The seed fields mirror CarveOutRequest (the
// union of explicit ids, a query, and named collections, at least one
// required); only the destination semantics differ -- the target must
// already exist and is never overwritten.
type CarveAddRequest struct {
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

	// Destination. An EXISTING store. DestDataDir is the absolute path to
	// that store's data directory, which MUST already exist. DestName is
	// an optional human-facing store name, validated and echoed back.
	DestName    string `json:"dest_name,omitempty"`
	DestDataDir string `json:"dest_data_dir,omitempty" jsonschema:"absolute path to the existing destination store's data directory"`

	// Options. There is deliberately NO read_only: the destination's
	// existing frozen state is preserved -- a frozen target is thawed for
	// the add and re-frozen to its exact prior state.
	HeadsOnly bool `json:"heads_only,omitempty" jsonschema:"skip the supersedes closure (do not pull superseded predecessors)"`
	DryRun    bool `json:"dry_run,omitempty" jsonschema:"resolve and report what would be added without writing anything"`
}

// selection projects a CarveAddRequest onto the shared carveSelection.
func (req CarveAddRequest) selection() carveSelection {
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

// CarveAddResponse reports what a top-up resolved to and what it changed
// in the destination. On a DryRun the counts describe what WOULD change
// and nothing was written.
type CarveAddResponse struct {
	DryRun bool `json:"dry_run"`

	DestName    string `json:"dest_name,omitempty"`
	DestDataDir string `json:"dest_data_dir,omitempty"`

	// SeedCount is the size of the resolved seed set (before closure).
	// SelectedNodes is the size of the closed selection resolved from the
	// source (before skip-present).
	SeedCount     int `json:"seed_count"`
	SelectedNodes int `json:"selected_nodes"`

	// Node outcome: genuinely-new nodes added, and nodes skipped because
	// their ULID was already present in the destination.
	NodesAdded          int `json:"nodes_added"`
	NodesSkippedPresent int `json:"nodes_skipped_present"`

	// Edge outcome: new edges added, edges skipped because an equivalent
	// (same source+target+type) edge already existed, and edges dropped
	// because their target is absent from the final destination set.
	EdgesAdded          int             `json:"edges_added"`
	EdgesSkippedPresent int             `json:"edges_skipped_present"`
	DroppedTotal        int             `json:"dropped_total"`
	DroppedByType       map[string]int  `json:"dropped_by_type,omitempty"`
	DanglingSample      []CarveDangling `json:"dangling_sample,omitempty"`

	// Thawed is true when the destination was frozen and was thawed for
	// the add then re-frozen to its exact prior state.
	Thawed bool `json:"thawed_and_refrozen"`

	// LeftThawed is true when the destination was frozen and thawed for
	// the add but could NOT be re-frozen afterward (the manifest write
	// failed): the store is left WRITABLE and must be re-frozen manually
	// with `gramaton store freeze <name>`. Mutually exclusive with Thawed --
	// a caller is never told the store was cleanly re-frozen while it is
	// actually writable.
	LeftThawed bool `json:"left_thawed,omitempty"`

	// EmbeddingDim is the vector dimension of the incoming records (0 when
	// the selection carries no embeddings).
	EmbeddingDim int `json:"embedding_dim,omitempty"`
}

const CarveAddDescription = "Non-destructively add more of the source store (memory records + collections) into an EXISTING destination store, idempotently and additively. Same seeds as a carve -- the union of explicit record ids, a query, and named collections, expanded to structural children, collection schemas, and (unless heads_only) superseded predecessors. Records already present in the destination are skipped, edges are reconnected to already-present records and de-duplicated (a re-run adds nothing), and edges whose other endpoint is absent from the destination are dropped and reported. A frozen destination is thawed for the add and re-frozen to its exact prior state; the destination is never overwritten or deleted. Sessions are never carried; use dry_run to preview."

// CarveAdd additively copies more of the source store's selected subset
// into an EXISTING destination store. Idempotent: nodes already present
// (by ULID) are skipped and equivalent edges are de-duplicated, so a
// re-run is a no-op. Reconnects edges to already-present destination
// records and drops (with a report) edges whose other endpoint is not in
// the destination.
//
// Like CarveOut this READS the source store (never gated behind the
// source's read-only flag) and writes only to a SEPARATE destination on
// its own engine. It manages the DESTINATION's freeze state (thaw for the
// add, re-freeze to the exact prior manifest) transactionally, and NEVER
// deletes the pre-existing destination on failure -- a failed add leaves
// the destination's data intact and its freeze state restored.
func (a *API) CarveAdd(ctx context.Context, req CarveAddRequest) (CarveAddResponse, *APIError) {
	g, apiErr := a.carveGather(ctx, req.selection())
	if apiErr != nil {
		return CarveAddResponse{}, apiErr
	}
	return a.carveAddMaterialize(req, g)
}

// carveAddMaterialize applies a gathered selection to an EXISTING
// destination store. Validates the destination, guards the embedding
// dimension, and -- transactionally around any freeze state -- skips
// present nodes, adds new ones, and reconnects deduped interior edges.
func (a *API) carveAddMaterialize(req CarveAddRequest, g *carveGathered) (resp CarveAddResponse, apiErr *APIError) {
	if req.DestDataDir == "" {
		return CarveAddResponse{}, ErrMissing("dest_data_dir is required")
	}
	if !filepath.IsAbs(req.DestDataDir) {
		return CarveAddResponse{}, ErrInvalid("dest_data_dir must be an absolute path")
	}
	if req.DestName != "" {
		if err := store.ValidateName(req.DestName); err != nil {
			return CarveAddResponse{}, ErrInvalid(err.Error())
		}
	}

	// The destination store HOME (parent of the data dir, holding
	// config.yaml) MUST already exist -- top-up never creates a store.
	home := filepath.Dir(req.DestDataDir)
	if _, err := os.Stat(home); err != nil {
		if os.IsNotExist(err) {
			return CarveAddResponse{}, ErrNotFound("destination store does not exist")
		}
		a.log.Warn("carve add: stat destination home", "component", "carveout", "err", err)
		return CarveAddResponse{}, ErrInternal("failed to check destination")
	}

	// Read the destination's freeze state up front so we can restore it
	// EXACTLY (owner + published_at included) on every exit path.
	origManifest, err := core.ReadStoreManifest(req.DestDataDir)
	if err != nil {
		a.log.Warn("carve add: read destination manifest", "component", "carveout", "err", err)
		return CarveAddResponse{}, ErrInternal("failed to read destination store manifest")
	}
	wasFrozen := origManifest.ReadOnly

	resp = CarveAddResponse{
		DryRun:        req.DryRun,
		SeedCount:     g.seedCount,
		SelectedNodes: len(g.nodes),
		EmbeddingDim:  g.embedDim,
	}

	if req.DryRun {
		// Preview: open read-only (a frozen destination stays frozen),
		// compute the diff, write nothing and touch no freeze state.
		return a.carveAddPreview(home, req, g, resp)
	}

	// Commit path. Thaw a frozen destination so the engine opens writable,
	// then restore the EXACT original manifest on EVERY exit via defer --
	// including a panic between the thaw and the end. carveAddRefreeze
	// SURFACES a restore failure (destination left writable) instead of
	// swallowing it, so a clean re-frozen success is never reported for a
	// store that is actually still writable.
	if wasFrozen {
		if err := core.ThawStore(req.DestDataDir); err != nil {
			a.log.Warn("carve add: thaw destination", "component", "carveout", "err", err)
			return CarveAddResponse{}, ErrInternal("failed to thaw destination store")
		}
		defer func() {
			if rerr := a.carveAddRefreeze(req.DestDataDir, req.DestName, origManifest, &resp); rerr != nil && apiErr == nil {
				apiErr = rerr
			}
		}()
	}

	// Open the destination WRITABLE (it opened writable because the
	// manifest is thawed) and top it up.
	//
	// The /v1/store/add HTTP handler already refused this add if a live
	// server was serving the destination (the only holder of its bbolt
	// file lock), so LoadEngine does not contend here -- core.LoadEngine
	// opens bbolt with no lock timeout and would otherwise block forever.
	// A server that grabbed the lock in the sliver between that check and
	// this open would still block: a negligible window on a single-user
	// tool, the same TOCTOU the freeze/thaw commands already accept.
	destEng, err := core.LoadEngine(home)
	if err != nil {
		a.log.Warn("carve add: open destination engine", "component", "carveout", "err", err)
		return CarveAddResponse{}, ErrInternal("failed to open destination store")
	}

	// Dimension guard: incoming vectors must match the destination's
	// configured dimension (you cannot add vectors of a different width to
	// an existing index). Runs AFTER open, so a frozen destination
	// exercises the thaw -> fail -> refreeze restore path. embedDim == 0
	// (no incoming vectors) is always compatible.
	destDim := destEng.Config().Embedding.Dimension
	if g.embedDim != 0 && g.embedDim != destDim {
		_ = destEng.Close()
		return CarveAddResponse{}, ErrInvalid(fmt.Sprintf(
			"incoming embedding dimension %d does not match destination dimension %d; cannot add",
			g.embedDim, destDim))
	}

	originStore := a.storeName
	if originStore == "" {
		originStore = "(default)"
	}

	var nodesAdded, nodesSkipped, edgesAdded, edgesSkipped int
	var part carvePartition
	writeErr := destEng.WithWriteBatch("carve_add", func(ws *core.WriteSession) (bool, error) {
		dg := ws.Graph()

		// Pass 1: add genuinely-new nodes; skip any ULID already present
		// (never overwrite). Newly-added nodes get origin_store stamped,
		// mirroring create; skipped nodes are left exactly as they are.
		for _, cn := range g.nodes {
			if _, exists := dg.GetNode(cn.id); exists {
				nodesSkipped++
				continue
			}
			props := cn.props.Clone()
			props["origin_store"] = graph.StringProperty(originStore)
			dg.AddNodeWithID(cn.id, props)
			ws.IndexNode(cn.id, cn.content, cn.vec)
			nodesAdded++
		}

		// The final destination set = pre-existing nodes UNION the nodes
		// just added, which is exactly what dg.GetNode reports after pass 1.
		// Pass 2: reconnect interior edges (target present, deduped against
		// what the destination already holds); count dangling edges (target
		// absent from the final set).
		part = carvePartition{
			droppedByType: make(map[string]int),
			sample:        make([]CarveDangling, 0, carveDanglingSampleCap),
		}
		for _, e := range g.edges {
			if _, ok := dg.GetNode(e.dst); !ok {
				part.droppedTotal++
				part.droppedByType[e.etype]++
				if len(part.sample) < carveDanglingSampleCap {
					part.sample = append(part.sample, CarveDangling{
						SourceID: e.src, TargetID: e.dst, Type: e.etype,
					})
				}
				continue
			}
			if carveHasEquivEdge(dg, e.src, e.dst, e.etype) {
				edgesSkipped++
				continue
			}
			if _, err := ws.AddEdge(e.src, e.dst, e.etype, e.weight, e.props); err != nil {
				return false, fmt.Errorf("carve add edge %s->%s (%s): %w", e.src, e.dst, e.etype, err)
			}
			edgesAdded++
		}

		// mutated iff something was actually added: a full re-run adds
		// nothing, returns false, and skips the save -- the destination is
		// left byte-for-byte unchanged.
		if nodesAdded > 0 || edgesAdded > 0 {
			ws.AddAction(graph.CommitAction{Kind: graph.ActionCarveAdd})
			return true, nil
		}
		return false, nil
	})
	if writeErr != nil {
		_ = destEng.Close()
		a.log.Warn("carve add: populate destination", "component", "carveout", "err", writeErr)
		return CarveAddResponse{}, ErrInternal("failed to add to destination store")
	}

	// Close flushes vectors + commits. Must precede the re-freeze, which
	// the deferred carveAddRefreeze performs after this function returns.
	if err := destEng.Close(); err != nil {
		a.log.Warn("carve add: close destination engine", "component", "carveout", "err", err)
		return CarveAddResponse{}, ErrInternal("failed to finalize destination store")
	}

	resp.DestName = req.DestName
	resp.DestDataDir = req.DestDataDir
	resp.NodesAdded = nodesAdded
	resp.NodesSkippedPresent = nodesSkipped
	resp.EdgesAdded = edgesAdded
	resp.EdgesSkippedPresent = edgesSkipped
	resp.DroppedTotal = part.droppedTotal
	resp.DroppedByType = part.droppedByType
	resp.DanglingSample = part.sample
	resp.Thawed = wasFrozen
	return resp, nil
}

// carveAddRefreeze re-applies the destination's original (frozen) manifest
// after a top-up thawed it, and SURFACES a write failure instead of
// swallowing it. On failure the destination is left WRITABLE, so it clears
// resp.Thawed, sets resp.LeftThawed, logs loudly, and returns an
// ErrInternal telling the operator to re-freeze -- the caller must never be
// told the store was cleanly re-frozen while it is actually writable.
// Returns nil on a successful re-freeze. Split out from the deferred
// restore so the failure plumbing is unit-testable. Uses
// WriteStoreManifest (not FreezeStore) to restore the EXACT prior manifest,
// preserving the original owner and published_at rather than re-stamping.
func (a *API) carveAddRefreeze(dataDir, name string, orig core.StoreManifest, resp *CarveAddResponse) *APIError {
	if err := core.WriteStoreManifest(dataDir, orig); err != nil {
		a.log.Error("carve add: restore destination freeze state; store left THAWED",
			"component", "carveout", "data_dir", dataDir, "err", err)
		resp.Thawed = false
		resp.LeftThawed = true
		target := name
		if target == "" {
			target = dataDir
		}
		return ErrInternal(fmt.Sprintf(
			"records were added but the destination could not be re-frozen and is left WRITABLE; re-freeze it with: gramaton store freeze %s", target))
	}
	return nil
}

// carveAddPreview computes the top-up diff against the destination
// without writing anything. Opens the destination read-only (a frozen
// destination therefore stays frozen and no thaw happens), classifies the
// gathered payload, and returns the counts.
func (a *API) carveAddPreview(home string, req CarveAddRequest, g *carveGathered, resp CarveAddResponse) (CarveAddResponse, *APIError) {
	destEng, err := core.LoadEngine(home)
	if err != nil {
		a.log.Warn("carve add: open destination for dry-run", "component", "carveout", "err", err)
		return CarveAddResponse{}, ErrInternal("failed to open destination store")
	}
	defer func() { _ = destEng.Close() }()

	// Dimension guard even on a preview: a dry run that would fail on
	// commit should say so rather than promise an add that cannot happen.
	destDim := destEng.Config().Embedding.Dimension
	if g.embedDim != 0 && g.embedDim != destDim {
		return CarveAddResponse{}, ErrInvalid(fmt.Sprintf(
			"incoming embedding dimension %d does not match destination dimension %d; cannot add",
			g.embedDim, destDim))
	}

	destEng.RLock()
	dg := destEng.Graph()

	// Present set = gathered nodes (all end up in the destination) UNION
	// any pre-existing destination node that an edge points at. That is
	// exactly the membership the commit path's dg.GetNode reports after it
	// has added the new nodes.
	present := make(map[string]struct{}, len(g.gathered))
	for id := range g.gathered {
		present[id] = struct{}{}
	}
	var nodesAdded, nodesSkipped int
	for _, cn := range g.nodes {
		if _, exists := dg.GetNode(cn.id); exists {
			nodesSkipped++
		} else {
			nodesAdded++
		}
	}
	for _, e := range g.edges {
		if _, ok := dg.GetNode(e.dst); ok {
			present[e.dst] = struct{}{}
		}
	}
	part := carvePartitionEdges(g.edges, present)

	// Of the interior edges, those the destination already holds would be
	// skipped on commit; the rest would be added.
	var edgesAdded, edgesSkipped int
	for _, e := range part.interior {
		if carveHasEquivEdge(dg, e.src, e.dst, e.etype) {
			edgesSkipped++
		} else {
			edgesAdded++
		}
	}
	destEng.RUnlock()

	resp.DestName = req.DestName
	resp.DestDataDir = req.DestDataDir
	resp.NodesAdded = nodesAdded
	resp.NodesSkippedPresent = nodesSkipped
	resp.EdgesAdded = edgesAdded
	resp.EdgesSkippedPresent = edgesSkipped
	resp.DroppedTotal = part.droppedTotal
	resp.DroppedByType = part.droppedByType
	resp.DanglingSample = part.sample
	resp.Thawed = false // a dry run never touches the freeze state
	return resp, nil
}

// carveHasEquivEdge reports whether the graph already holds an edge with
// the same source, target, and type -- the equivalence used to de-dup
// edges on a top-up so a re-run adds no duplicates.
func carveHasEquivEdge(g *graph.Graph, src, dst, etype string) bool {
	for _, e := range g.EdgesFrom(src) {
		if e.TargetID == dst && e.Type == etype {
			return true
		}
	}
	return false
}
