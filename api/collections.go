package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/storage"
)

// --- helpers ---

// collectionByName finds a collection by name (case-insensitive).
// Caller must hold at least RLock.
func (a *API) collectionByName(name string) (string, bool) {
	ids := a.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("collection"))
	lower := strings.ToLower(name)
	for _, id := range ids {
		n, ok := a.engine.Graph().GetNode(id)
		if !ok {
			continue
		}
		if cname, ok := n.Properties.GetString("collection_name"); ok {
			if strings.ToLower(cname) == lower {
				return id, true
			}
		}
	}
	return "", false
}

// collectionItemEdges returns all member_of edges pointing to the collection.
// Caller must hold at least RLock.
func (a *API) collectionItemEdges(collectionID string) []*graph.Edge {
	// Use the collection cache for O(1) member lookup when available.
	if cc := a.engine.CollCache(); cc != nil {
		members := cc.Members(collectionID)
		if len(members) > 0 {
			var edges []*graph.Edge
			for _, memberID := range members {
				for _, e := range a.engine.Graph().EdgesFrom(memberID) {
					if e.Type == "member_of" && e.TargetID == collectionID {
						edges = append(edges, e)
						break
					}
				}
			}
			return edges
		}
		// Cache is empty -- fall through to edge scan (cold start).
	}
	var edges []*graph.Edge
	for _, e := range a.engine.Graph().EdgesTo(collectionID) {
		if e.Type == "member_of" {
			edges = append(edges, e)
		}
	}
	return edges
}

// isCollection checks if a node is a collection.
// Caller must hold at least RLock.
func (a *API) isCollection(nodeID string) (*graph.Node, *APIError) {
	n, ok := a.engine.Graph().GetNode(nodeID)
	if !ok {
		return nil, ErrNotFound("collection not found")
	}
	kt, _ := n.Properties.GetString("knowledge_type")
	if kt != "collection" {
		return nil, ErrNotFound("not a collection")
	}
	return n, nil
}

// isRetired checks if a collection has a valid_until in the past.
func isRetired(n *graph.Node) bool {
	vu, ok := n.Properties.GetTimestamp("valid_until")
	if !ok {
		return false
	}
	return vu.Before(time.Now())
}

// loadSchema loads and parses the schema from a collection node.
func loadSchema(n *graph.Node) (*CollectionSchema, error) {
	raw, ok := n.Properties.GetString("collection_schema")
	if !ok {
		return nil, nil
	}
	return parseCollectionSchema(raw)
}

// setFieldProps stores field.* properties on a node from a fields map.
// Caller must hold the write lock.
func (a *API) setFieldProps(nodeID string, fields map[string]any) {
	for k, v := range fields {
		propKey := "field." + k
		switch val := v.(type) {
		case string:
			a.engine.SetProp(nodeID, propKey, graph.StringProperty(val))
		case float64:
			a.engine.SetProp(nodeID, propKey, graph.Float64Property(val))
		case bool:
			a.engine.SetProp(nodeID, propKey, graph.BoolProperty(val))
		case nil:
			// Explicit null -- remove the property if it exists.
			a.engine.SetProp(nodeID, propKey, graph.StringProperty(""))
		case []any:
			// enum[] -- store as StringList. Schema validation has
			// already enforced string elements for typed collections;
			// for schema-less collections, drop anything that slipped
			// through as a non-string rather than panic inside a
			// write-lock hold. validateItemFields mirrors this guard
			// for schema-less items.
			ss := make([]string, 0, len(val))
			for _, elem := range val {
				if s, ok := elem.(string); ok {
					ss = append(ss, s)
				}
			}
			a.engine.SetProp(nodeID, propKey, graph.StringListProperty(ss))
		}
	}
}

// setFieldPropsIn is setFieldProps routed through a WriteSession so
// property writes share the batch's bbolt tx instead of opening one
// per call. Used inside WithWriteBatch closures. (P2-06.)
func (a *API) setFieldPropsIn(ws *core.WriteSession, nodeID string, fields map[string]any) {
	for k, v := range fields {
		propKey := "field." + k
		switch val := v.(type) {
		case string:
			ws.SetProp(nodeID, propKey, graph.StringProperty(val))
		case float64:
			ws.SetProp(nodeID, propKey, graph.Float64Property(val))
		case bool:
			ws.SetProp(nodeID, propKey, graph.BoolProperty(val))
		case nil:
			ws.SetProp(nodeID, propKey, graph.StringProperty(""))
		case []any:
			ss := make([]string, 0, len(val))
			for _, elem := range val {
				if s, ok := elem.(string); ok {
					ss = append(ss, s)
				}
			}
			ws.SetProp(nodeID, propKey, graph.StringListProperty(ss))
		}
	}
}

// extractFields reads field.* properties from a node into a map.
func extractFields(n *graph.Node) map[string]any {
	fields := make(map[string]any)
	for k, v := range n.Properties {
		if !strings.HasPrefix(k, "field.") {
			continue
		}
		name := strings.TrimPrefix(k, "field.")
		switch v.Type {
		case graph.TypeString:
			fields[name] = v.String()
		case graph.TypeFloat64:
			fields[name] = v.Float64()
		case graph.TypeBool:
			fields[name] = v.Bool()
		case graph.TypeInt64:
			fields[name] = v.Int64()
		case graph.TypeTimestamp:
			fields[name] = v.Timestamp().Format(time.RFC3339)
		case graph.TypeStringList:
			fields[name] = v.StringList()
		}
	}
	return fields
}

// isMemberOf checks if itemID has a member_of edge to collectionID.
// Returns the edge if found.
func (a *API) isMemberOf(itemID, collectionID string) (*graph.Edge, bool) {
	for _, e := range a.engine.Graph().EdgesFrom(itemID) {
		if e.Type == "member_of" && e.TargetID == collectionID {
			return e, true
		}
	}
	return nil, false
}

// nodeCollectionNames returns the names of all collections a node belongs to.
// Caller must hold at least RLock.
func (a *API) nodeCollectionNames(nodeID string) []string {
	var names []string
	for _, e := range a.engine.Graph().EdgesFrom(nodeID) {
		if e.Type != "member_of" {
			continue
		}
		n, ok := a.engine.Graph().GetNode(e.TargetID)
		if !ok {
			continue
		}
		if name, ok := n.Properties.GetString("collection_name"); ok {
			names = append(names, name)
		}
	}
	return names
}

// joinCollectionNames formats a list of collection names for display.
func joinCollectionNames(names []string) string {
	return strings.Join(names, ", ")
}

// Description constants shared by every transport (HTTP binding MCP
// tool, CLI MCP proxy). Keeping them with the api package is the T-02
// anti-drift convention: one source of truth for "what does this tool
// do", so the server-registered description and the CLI proxy's
// description can't diverge over time.

const CollectionCreateDescription = "Create a new collection. Collections provide structured, exhaustive retrieval -- every item is always returned. Use for tasks, backlogs, reading lists, checklists. Use Memory (gramaton_capture) for semantic knowledge like decisions, context, and research."

const CollectionListDescription = "List collections with names, item counts, and schema status. Returns {showing, total, has_more, next_offset} for pagination. Call again with offset=next_offset to get the next page."

const CollectionItemsDescription = "List items in a collection. Returns every item matching the filter, guaranteed complete (no pagination). Supports sorting by any field. Use `fields` to project a subset of schema fields (e.g. [\"title\",\"status\"]) and `filter` to narrow by exact schema-field match (e.g. {\"status\":\"open\"} or {\"severity\":[\"P1\",\"P2\"]})."

const CollectionAddDescription = "Add an item to a collection. Use for tasks, TODOs, action items, or any structured data that needs exhaustive tracking. Fields are validated against the collection's schema. Returns ErrConflict if an item with the same title already exists in the collection."

const CollectionUpdateDescription = "Update fields on a collection item. Existing fields are preserved; only specified fields are changed. Validated against the collection schema."

const CollectionMoveDescription = "Move an item from one collection to another. The item's fields are validated against the target collection's schema."

const CollectionRemoveDescription = "Remove an item from a collection. The item node is preserved in the graph; only the membership edge is deleted."

const CollectionRenameDescription = "Rename a collection. Name must be unique."

const CollectionDeleteDescription = "Retire a collection (reversible). Items and edges are preserved. Call again on a retired collection to re-activate it."

const CollectionSchemaDescription = "Read a collection's schema and migration status."

const CollectionMigrateDescription = "Bulk-update items for a schema migration. Sets the specified field on all items that are missing it. Required after adding a new required field to a schema."

const CollectionAddBatchDescription = "Add many items to a collection in a single call. Items are schema-validated and dedup-checked individually; items that pass commit atomically in one engine save, items that fail are reported per-item in the Failed array. Use instead of repeated gramaton_collection_add when loading more than ~10 items. Max 500 items per call. Returns per-item {index, client_ref, id} on success and {index, client_ref, code, message} on failure."

// --- service methods ---

type CollectionCreateRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Schema      *CollectionSchema `json:"schema,omitempty"`

	// Behaviour knobs (Phase 4). All three are optional -- absent
	// means "use the default", via the read-time fallback in
	// collection_config.go. Passing an explicit value stores it on
	// the collection node so future reads surface it without the
	// fallback (useful for visibility).
	ClearMode    string `json:"clear_mode,omitempty"`
	Supersession string `json:"supersession,omitempty"`
	Curation     string `json:"curation,omitempty"`
}

func (a *API) CollectionCreate(_ context.Context, req *CollectionCreateRequest) (map[string]any, *APIError) {
	if err := validateCollectionName(req.Name); err != nil {
		return nil, ErrInvalid(err.Error())
	}
	if err := validateSchema(req.Schema); err != nil {
		return nil, ErrInvalid(err.Error())
	}
	if err := validateCollectionConfig(req.ClearMode, req.Supersession, req.Curation); err != nil {
		return nil, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	// Enforce name uniqueness.
	if _, exists := a.collectionByName(req.Name); exists {
		return nil, ErrConflict(fmt.Sprintf("collection %q already exists", req.Name))
	}

	props := graph.Properties{
		"knowledge_type":    graph.StringProperty("collection"),
		"collection_name":   graph.StringProperty(req.Name),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"processing_status": graph.StringProperty("processed"),
		"access_count":      graph.Int64Property(0),
	}
	if req.Description != "" {
		props["collection_description"] = graph.StringProperty(req.Description)
	}
	if req.Schema != nil {
		raw, err := serializeCollectionSchema(req.Schema)
		if err != nil {
			a.log.Warn("collection schema serialize failed", "component", "collection", "err", err)
			return nil, ErrInternal("failed to serialize schema")
		}
		props["collection_schema"] = graph.StringProperty(raw)
	}
	if req.ClearMode != "" {
		props[propClearMode] = graph.StringProperty(req.ClearMode)
	}
	if req.Supersession != "" {
		props[propSupersession] = graph.StringProperty(req.Supersession)
	}
	if req.Curation != "" {
		props[propCuration] = graph.StringProperty(req.Curation)
	}

	n := a.engine.Graph().AddNode(props)
	bm25Text := req.Name
	if req.Description != "" {
		bm25Text += " " + req.Description
	}
	a.engine.IndexNode(n.ID, bm25Text, nil)

	if _, err := a.engine.Save("collection_create"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "create", "err", err)
		return nil, ErrInternal("failed to save collection")
	}

	return map[string]any{"id": n.ID, "name": req.Name}, nil
}

type CollectionListRequest struct {
	Limit  int
	Offset int
}

func (a *API) CollectionList(ctx context.Context, req *CollectionListRequest) (map[string]any, *APIError) {
	_ = ctx
	a.engine.RLock()
	defer a.engine.RUnlock()

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	ids := a.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("collection"))
	all := make([]map[string]any, 0, len(ids))

	for _, id := range ids {
		n, ok := a.engine.Graph().GetNode(id)
		if !ok {
			continue
		}
		name, _ := n.Properties.GetString("collection_name")
		if name == "" {
			continue
		}

		entry := map[string]any{
			"id":   id,
			"name": name,
		}
		if desc, ok := n.Properties.GetString("collection_description"); ok {
			entry["description"] = desc
		}
		if _, ok := n.Properties.GetString("collection_schema"); ok {
			entry["has_schema"] = true
		}
		entry["item_count"] = len(a.collectionItemEdges(id))

		if isRetired(n) {
			entry["retired"] = true
		}
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			entry["created_at"] = ca.Format(time.RFC3339)
		}

		all = append(all, entry)
	}

	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := all[offset:end]

	result := map[string]any{
		"collections": page,
		"showing":     len(page),
		"total":       total,
	}
	if end < total {
		result["has_more"] = true
		result["next_offset"] = end
	}
	return result, nil
}

type CollectionItemsRequest struct {
	Sort           string `json:"sort,omitempty"`
	Order          string `json:"order,omitempty"`
	IncludeRetired bool   `json:"include_retired,omitempty"`

	// Fields is a allowlist of schema field names to include in each
	// item's `fields` sub-map. Empty/nil means return every field
	// (today's behavior). `id`, `created_at`, and `needs_migration`
	// are always included at the top level regardless of this list.
	Fields []string `json:"fields,omitempty"`

	// Filter is a schema-field -> expected-value(s) map that keeps an
	// item only when every listed field matches. Values may be a
	// single string for exact match, or a []string / []any of strings
	// for "any-of" match (OR within a field, AND across fields).
	// Unknown field names match nothing (empty result). Filtering
	// preserves the exhaustive-retrieval contract because matches are
	// exact, not ranked.
	Filter map[string]any `json:"filter,omitempty"`

	// AsOf, when set, returns point-in-time membership: the members
	// that had `member_of` edges to the collection at the commit
	// immediately before AsOf, with each member's state at that
	// commit. Response carries `as_of` + `semantics: "point_in_time"`
	// so agents don't have to guess. Accepts YYYY-MM-DD or RFC3339.
	// Future dates are rejected. The filter/sort/projection knobs
	// still apply, but migration accounting is skipped (historical
	// snapshots are read-only).
	AsOf string `json:"as_of,omitempty"`
}

// CollectionItems is deliberately unpaginated -- exhaustive retrieval is the
// contract that distinguishes Collections from Memory. If a collection
// grows large enough to need pagination, it's a signal to split it.
// Filter narrows the result by exact schema-field match (preserving the
// exhaustive contract). Fields projects the per-item `fields` sub-map
// down to a allowlist -- both are there so agents can audit large
// collections without dragging the full-fidelity `details` payload.
//
// When req.AsOf is set, CollectionItems switches to point-in-time mode:
// the response reflects the commit at-or-before AsOf (D7 CommitAt), and
// each member is read at its per-commit state. The response carries
// `as_of` + `semantics: "point_in_time"` so agents don't have to guess.
func (a *API) CollectionItems(ctx context.Context, collectionID string, req *CollectionItemsRequest) (map[string]any, *APIError) {
	_ = ctx
	asOfT, asOfErr := validateAsOf(req.AsOf, nil)
	if asOfErr != nil {
		return nil, ErrInvalid(asOfErr.Error())
	}
	filterMatchers, filterErr := buildFilterMatchers(req.Filter)
	if filterErr != nil {
		return nil, ErrInvalid(filterErr.Error())
	}
	projection, projErr := normalizeProjection(req.Fields)
	if projErr != nil {
		return nil, ErrInvalid(projErr.Error())
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	// Point-in-time branch: read the collection membership at the
	// historical commit. No interaction with the live graph state --
	// everything flows through the CAS store via the commit's prolly
	// tree roots.
	if !asOfT.IsZero() {
		return a.collectionItemsAtCommit(collectionID, asOfT, filterMatchers, projection, req)
	}

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if !req.IncludeRetired && isRetired(coll) {
		return nil, ErrNotFound("collection is retired")
	}

	// Check migration state.
	var migration map[string]any
	if migFields, ok := coll.Properties.GetStringList("collection_migration_fields"); ok && len(migFields) > 0 {
		total, _ := coll.Properties.GetInt64("collection_migration_total")
		migration = map[string]any{
			"fields": migFields,
			"total":  total,
		}
	}

	edges := a.collectionItemEdges(collectionID)
	items := make([]map[string]any, 0, len(edges))
	// Track migration progress over the full (pre-filter) set so
	// done/remaining counts reflect the collection, not whatever
	// subset the caller filtered to.
	var migDone, migRemaining int

	for _, e := range edges {
		n, ok := a.engine.Graph().GetNode(e.SourceID)
		if !ok {
			continue
		}
		fullFields := extractFields(n)

		// Migration accounting on the full set.
		var migMissing []string
		if migration != nil {
			if migFields, ok := migration["fields"].([]string); ok {
				for _, f := range migFields {
					if _, has := n.Properties["field."+f]; !has {
						migMissing = append(migMissing, f)
					}
				}
				if len(migMissing) > 0 {
					migRemaining++
				} else {
					migDone++
				}
			}
		}

		// Filter check runs against the full field map, not the
		// projection. Filtering on a field the caller didn't project
		// must still work.
		if len(filterMatchers) > 0 && !matchesFilter(fullFields, filterMatchers) {
			continue
		}

		projectedFields := fullFields
		if projection != nil {
			projectedFields = make(map[string]any, len(projection))
			for name := range projection {
				if v, ok := fullFields[name]; ok {
					projectedFields[name] = v
				}
			}
		}

		item := map[string]any{
			"id":     e.SourceID,
			"fields": projectedFields,
		}
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			item["created_at"] = ca.Format(time.RFC3339)
		}
		if len(migMissing) > 0 {
			item["needs_migration"] = migMissing
		}

		items = append(items, item)
	}

	// Sort items. Sort uses the full field map where needed -- the
	// projection above may have dropped the sort key, but we stashed
	// the value on the item when assembling it. For non-projected
	// fields we re-extract from the node in the sort comparator, but
	// that would require re-locking / re-fetching; simpler: keep the
	// sort key in the item if it isn't in the projection.
	sortField := req.Sort
	descending := req.Order == "desc"
	if sortField == "" {
		sortField = "created_at"
	}
	if sortField != "created_at" && projection != nil {
		if _, projected := projection[sortField]; !projected {
			// The caller excluded the sort field from projection; add
			// it back onto the stashed fields just for ordering. This
			// keeps sort semantics consistent with non-projected calls.
			for _, item := range items {
				f, _ := item["fields"].(map[string]any)
				if f == nil {
					f = map[string]any{}
					item["fields"] = f
				}
				if _, present := f[sortField]; present {
					continue
				}
				if n, ok := a.engine.Graph().GetNode(item["id"].(string)); ok {
					if v, ok := extractFields(n)[sortField]; ok {
						f[sortField] = v
					}
				}
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		var vi, vj any
		if sortField == "created_at" {
			vi = items[i]["created_at"]
			vj = items[j]["created_at"]
		} else {
			if fi, ok := items[i]["fields"].(map[string]any); ok {
				vi = fi[sortField]
			}
			if fj, ok := items[j]["fields"].(map[string]any); ok {
				vj = fj[sortField]
			}
		}
		less := compareAny(vi, vj)
		if descending {
			return !less
		}
		return less
	})
	// Strip the sort-only field re-adds so the wire output actually
	// honors the projection allowlist.
	if sortField != "created_at" && projection != nil {
		if _, projected := projection[sortField]; !projected {
			for _, item := range items {
				if f, ok := item["fields"].(map[string]any); ok {
					delete(f, sortField)
				}
			}
		}
	}

	result := map[string]any{
		"collection_id": collectionID,
		"items":         items,
		"count":         len(items),
	}
	if migration != nil {
		migration["done"] = migDone
		migration["remaining"] = migRemaining
		result["migration"] = migration
	}

	return result, nil
}

// collectionItemsAtCommit answers "what members did this collection
// have at commit CommitAt(asOf), and what was each member's state
// then?" It reads only the CAS store and the timestamp index -- the
// live graph is never touched, so concurrent writes and HEAD
// mutations don't affect the snapshot. Response shape mirrors the
// HEAD path plus `as_of` + `semantics: "point_in_time"`.
//
// Caller must hold at least RLock.
func (a *API) collectionItemsAtCommit(
	collectionID string,
	asOfT time.Time,
	filterMatchers map[string]map[string]struct{},
	projection map[string]struct{},
	req *CollectionItemsRequest,
) (map[string]any, *APIError) {
	tsIdx := a.engine.TSIndex()
	commitHash, ok := tsIdx.CommitAt(asOfT)
	if !ok {
		return emptyHistoricalResponse(collectionID, asOfT), nil
	}
	store := a.engine.Store()

	commit, err := graph.LoadCommitMeta(store, commitHash)
	if err != nil {
		a.log.Warn("collection_items as_of: load commit",
			"component", "collections", "commit", commitHash, "err", err)
		return nil, ErrInternal("failed to load historical commit")
	}

	// The collection itself must exist at this commit; otherwise the
	// caller asked about a point in time before the collection was
	// created (or after it was hard-deleted -- no such path today).
	collHash, collFound, err := graph.NodeHashInCommit(store, commitHash, collectionID)
	if err != nil {
		return nil, ErrInternal("failed to resolve collection at commit")
	}
	if !collFound {
		return emptyHistoricalResponse(collectionID, asOfT), nil
	}
	collData, err := store.Read(collHash)
	if err != nil {
		return nil, ErrInternal("failed to read collection node")
	}
	collNode, err := graph.UnmarshalNode(collData)
	if err != nil {
		return nil, ErrInternal("failed to unmarshal collection node")
	}
	kt, _ := collNode.Properties.GetString("knowledge_type")
	if kt != "collection" {
		return nil, ErrNotFound("not a collection")
	}
	if !req.IncludeRetired {
		if vu, ok := collNode.Properties.GetTimestamp("valid_until"); ok && vu.Before(asOfT) {
			return nil, ErrNotFound("collection is retired")
		}
	}

	// Walk the edge tree at this commit to find member_of edges
	// pointing at the collection. Each edge's CAS entry has the
	// source ID (the member).
	if commit.EdgeTreeRoot == "" {
		return emptyHistoricalResponse(collectionID, asOfT), nil
	}
	edgeTree := storage.LoadProllyTree(store, commit.EdgeTreeRoot)
	edgeEntries, err := edgeTree.AllEntries()
	if err != nil {
		return nil, ErrInternal("failed to read edge tree at commit")
	}

	var memberIDs []string
	for _, e := range edgeEntries {
		data, readErr := store.Read(e.Value)
		if readErr != nil {
			continue
		}
		edge, unmErr := graph.UnmarshalEdge(data)
		if unmErr != nil {
			continue
		}
		if edge.Type != "member_of" || edge.TargetID != collectionID {
			continue
		}
		memberIDs = append(memberIDs, edge.SourceID)
	}

	items := make([]map[string]any, 0, len(memberIDs))
	for _, mid := range memberIDs {
		nodeHash, found, nhErr := graph.NodeHashInCommit(store, commitHash, mid)
		if nhErr != nil || !found {
			continue
		}
		data, readErr := store.Read(nodeHash)
		if readErr != nil {
			continue
		}
		n, unmErr := graph.UnmarshalNode(data)
		if unmErr != nil {
			continue
		}
		fullFields := extractFields(n)

		if len(filterMatchers) > 0 && !matchesFilter(fullFields, filterMatchers) {
			continue
		}

		projectedFields := fullFields
		if projection != nil {
			projectedFields = make(map[string]any, len(projection))
			for name := range projection {
				if v, ok := fullFields[name]; ok {
					projectedFields[name] = v
				}
			}
		}

		item := map[string]any{
			"id":     mid,
			"fields": projectedFields,
		}
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			item["created_at"] = ca.Format(time.RFC3339)
		}
		items = append(items, item)
	}

	sortField := req.Sort
	if sortField == "" {
		sortField = "created_at"
	}
	descending := req.Order == "desc"
	sort.SliceStable(items, func(i, j int) bool {
		var vi, vj any
		if sortField == "created_at" {
			vi = items[i]["created_at"]
			vj = items[j]["created_at"]
		} else {
			if fi, ok := items[i]["fields"].(map[string]any); ok {
				vi = fi[sortField]
			}
			if fj, ok := items[j]["fields"].(map[string]any); ok {
				vj = fj[sortField]
			}
		}
		less := compareAny(vi, vj)
		if descending {
			return !less
		}
		return less
	})

	resp := map[string]any{
		"collection_id": collectionID,
		"items":         items,
		"count":         len(items),
		"as_of":         asOfT.Format(time.RFC3339),
		"semantics":     "point_in_time",
	}
	return resp, nil
}

// emptyHistoricalResponse returns the empty-result shape for an as_of
// read when the collection didn't exist at that point or the index
// has no commit at-or-before the requested time. Keeps the semantic-
// naming contract (`as_of` + `semantics`) consistent whether or not
// data was found.
func emptyHistoricalResponse(collectionID string, asOfT time.Time) map[string]any {
	return map[string]any{
		"collection_id": collectionID,
		"items":         []map[string]any{},
		"count":         0,
		"as_of":         asOfT.Format(time.RFC3339),
		"semantics":     "point_in_time",
	}
}

// buildFilterMatchers validates the filter request and returns a map of
// field-name -> allowed-value-set. Each item passes when, for every
// matcher, its field value is contained in the allowed set. Values are
// compared as strings (most schema fields are strings or enums; the
// filter here is deliberately coarse, not a query DSL). Bounded by
// MaxFilterKeys and MaxFilterValuesPerKey to prevent unbounded
// attacker-controlled allocation.
func buildFilterMatchers(filter map[string]any) (map[string]map[string]struct{}, error) {
	if len(filter) == 0 {
		return nil, nil
	}
	if len(filter) > MaxFilterKeys {
		return nil, fmt.Errorf("filter: maximum %d keys allowed", MaxFilterKeys)
	}
	out := make(map[string]map[string]struct{}, len(filter))
	for key, raw := range filter {
		if !fieldNameRe.MatchString(key) {
			return nil, fmt.Errorf("filter key %q contains invalid characters", key)
		}
		allowed := make(map[string]struct{})
		addValue := func(name string, s string) error {
			if len(allowed) >= MaxFilterValuesPerKey {
				return fmt.Errorf("filter.%s: maximum %d values allowed", name, MaxFilterValuesPerKey)
			}
			allowed[s] = struct{}{}
			return nil
		}
		switch v := raw.(type) {
		case string:
			if err := addValue(key, v); err != nil {
				return nil, err
			}
		case []string:
			for _, s := range v {
				if err := addValue(key, s); err != nil {
					return nil, err
				}
			}
		case []any:
			for i, elem := range v {
				s, ok := elem.(string)
				if !ok {
					return nil, fmt.Errorf("filter.%s[%d]: expected string, got %T", key, i, elem)
				}
				if err := addValue(key, s); err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("filter.%s: expected string or []string, got %T", key, raw)
		}
		if len(allowed) == 0 {
			return nil, fmt.Errorf("filter.%s: at least one value required", key)
		}
		out[key] = allowed
	}
	return out, nil
}

// matchesFilter returns true when every matcher hits a value in the
// item's fields. Values are coerced to their canonical string form via
// fmt.Sprint so numeric and bool schema fields still compare correctly
// against stringly-typed filter values.
func matchesFilter(fields map[string]any, matchers map[string]map[string]struct{}) bool {
	for key, allowed := range matchers {
		val, ok := fields[key]
		if !ok {
			return false
		}
		if _, hit := allowed[fmt.Sprint(val)]; !hit {
			return false
		}
	}
	return true
}

// normalizeProjection returns a set (map-with-empty-struct values) of
// schema field names the caller asked for, or nil when projection is
// disabled. An empty slice is treated as "no projection" so a
// forgotten query string doesn't silently strip everything. Bounded by
// MaxProjectionFields; field names must match fieldNameRe (matching the
// rule for schema-declared field names) so malformed inputs fail fast.
func normalizeProjection(fields []string) (map[string]struct{}, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	if len(fields) > MaxProjectionFields {
		return nil, fmt.Errorf("fields: maximum %d entries allowed", MaxProjectionFields)
	}
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		if !fieldNameRe.MatchString(f) {
			return nil, fmt.Errorf("fields: %q contains invalid characters", f)
		}
		out[f] = struct{}{}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

type CollectionAddRequest struct {
	Fields map[string]any `json:"fields"`
}

func (a *API) CollectionAdd(ctx context.Context, collectionID string, req *CollectionAddRequest) (map[string]any, *APIError) {
	if len(req.Fields) == 0 {
		return nil, ErrMissing("fields are required")
	}

	// Pre-embed field text for graph connectivity (outside lock).
	var itemVec []float32
	if a.engine.Embedder() != nil {
		var textParts []string
		for _, v := range req.Fields {
			if str, ok := v.(string); ok && str != "" {
				textParts = append(textParts, str)
			}
		}
		if len(textParts) > 0 {
			embedText := strings.Join(textParts, " ")
			if vecs, err := a.engine.Embedder().Embed(ctx, []string{embedText}); err == nil && len(vecs) > 0 {
				itemVec = vecs[0]
			}
		}
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if isRetired(coll) {
		return nil, ErrInvalid("cannot add to retired collection")
	}

	// Schema validation.
	schema, err := loadSchema(coll)
	if err != nil {
		a.log.Warn("collection schema load failed", "component", "collection", "err", err)
		return nil, ErrInternal("failed to load schema")
	}
	if err := validateItemFields(schema, req.Fields); err != nil {
		return nil, ErrInvalid(err.Error())
	}

	// Dedup check: look for existing item with same title. A duplicate
	// is a state conflict (the caller's add does not commit) rather
	// than a partial success, so return ErrConflict with the existing
	// ID in the message. Contract: non-nil APIError iff the op did not
	// commit. Matches Capture's reject-mode semantics.
	if title, ok := req.Fields["title"]; ok {
		titleStr, isStr := title.(string)
		if isStr && titleStr != "" {
			for _, e := range a.collectionItemEdges(collectionID) {
				n, ok := a.engine.Graph().GetNode(e.SourceID)
				if !ok {
					continue
				}
				if existing, ok := n.Properties.GetString("field.title"); ok {
					if strings.EqualFold(existing, titleStr) {
						return nil, ErrConflict(fmt.Sprintf("item with title %q already exists in this collection (existing id: %s)", titleStr, e.SourceID))
					}
				}
			}
		}
	}

	// Create item node.
	props := graph.Properties{
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
		"access_count": graph.Int64Property(0),
	}
	n := a.engine.Graph().AddNode(props)
	a.setFieldProps(n.ID, req.Fields)

	// Index for BM25 using field values.
	var bm25Parts []string
	for _, v := range req.Fields {
		if s, ok := v.(string); ok {
			bm25Parts = append(bm25Parts, s)
		}
	}
	a.engine.IndexNode(n.ID, strings.Join(bm25Parts, " "), itemVec)
	if itemVec != nil && a.engine.Embedder() != nil {
		a.engine.SetProp(n.ID, "embedding_model", graph.StringProperty(a.engine.Embedder().ModelID()))
	}

	// Create member_of edge.
	if _, err := a.engine.Graph().AddEdge(n.ID, collectionID, "member_of", 1.0, nil); err != nil {
		a.log.Warn("member_of edge create failed", "component", "collection",
			"collection_id", collectionID, "item_id", n.ID, "err", err)
		return nil, ErrInternal("failed to add item to collection")
	}
	if cc := a.engine.CollCache(); cc != nil {
		cc.AddMember(collectionID, n.ID)
	}

	if _, err := a.engine.Save("collection_add"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "add", "err", err)
		return nil, ErrInternal("failed to save collection item")
	}

	return map[string]any{"id": n.ID, "collection_id": collectionID}, nil
}

// CollectionAddItem is one item inside a CollectionAddBatchRequest.
// The optional ClientRef is echoed back in the per-item result so
// callers can correlate outcomes with their own records without
// relying on positional order.
type CollectionAddItem struct {
	Fields    map[string]any `json:"fields" jsonschema:"item fields (must match collection schema if defined)"`
	ClientRef string         `json:"client_ref,omitempty" jsonschema:"optional caller handle echoed in the per-item result"`
}

type CollectionAddBatchRequest struct {
	Items []CollectionAddItem `json:"items" jsonschema:"array of items to add (max 500)"`
}

// BatchAddSuccess is one entry in CollectionAddBatchResponse.Added.
// Exactly one of {ID} or {Code, Message} is populated per item.
type BatchAddSuccess struct {
	Index     int    `json:"index"`
	ClientRef string `json:"client_ref,omitempty"`
	ID        string `json:"id"`
}

// BatchAddFailure is one entry in CollectionAddBatchResponse.Failed.
// Code matches the APIError codes ("input_error", "duplicate", etc.).
type BatchAddFailure struct {
	Index     int    `json:"index"`
	ClientRef string `json:"client_ref,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type CollectionAddBatchResponse struct {
	CollectionID string            `json:"collection_id"`
	Added        []BatchAddSuccess `json:"added"`
	Failed       []BatchAddFailure `json:"failed"`
}

// CollectionAddBatch adds many items to a collection in one call. The
// implementation runs in two phases:
//
//   Phase 1 (off-lock): schema-validate every item and batch-embed
//   the concatenated text per item in a single provider call. Per-
//   item validation failures are recorded without engaging the
//   engine. An embed-call failure is tolerated -- items that would
//   have been embedded fall back to embed-less add, preserving the
//   rest of the batch.
//
//   Phase 2 (write lock): load the collection + schema, run the
//   dedup pass (existing members via CollCache plus intra-batch
//   titles) and commit all passing items inside a single
//   BatchIndexWrites transaction. One Save at the end.
//
// Best-effort semantics: per-item validation and dedup failures are
// reported in Failed; items that pass pre-checks commit atomically.
// A Save-phase failure aborts the whole batch with a top-level
// APIError; it does not produce partial per-item results.
func (a *API) CollectionAddBatch(ctx context.Context, collectionID string, req CollectionAddBatchRequest) (CollectionAddBatchResponse, *APIError) {
	if len(req.Items) == 0 {
		return CollectionAddBatchResponse{}, ErrMissing("items is required")
	}
	if len(req.Items) > MaxCollectionBatchSize {
		return CollectionAddBatchResponse{}, ErrInvalid(fmt.Sprintf("items exceeds %d; split into smaller batches", MaxCollectionBatchSize))
	}

	// Phase 1: schema-validate off-lock. Items that fail validation
	// short-circuit here and never touch the engine. Passing items
	// accumulate in survivors with their original request index for
	// stable result ordering.
	type prepared struct {
		reqIdx  int
		item    CollectionAddItem
		vec     []float32
		vecText string // concatenated string fields; empty if none
	}
	// Schema lookup has to wait for the write lock so we don't race
	// with CollectionSchemaUpdate. Validate the per-item SHAPE
	// (non-empty Fields) here; defer schema conformance to phase 2.
	failed := make([]BatchAddFailure, 0)
	survivors := make([]prepared, 0, len(req.Items))
	for i, item := range req.Items {
		if len(item.Fields) == 0 {
			failed = append(failed, BatchAddFailure{
				Index:     i,
				ClientRef: item.ClientRef,
				Code:      "input_error",
				Message:   "fields is required",
			})
			continue
		}
		var parts []string
		for _, v := range item.Fields {
			if s, ok := v.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		survivors = append(survivors, prepared{
			reqIdx:  i,
			item:    item,
			vecText: strings.Join(parts, " "),
		})
	}

	// Phase 1 (continued): batch-embed survivors' text. One provider
	// call instead of N. An embed error is logged and tolerated --
	// we proceed with empty vectors, matching the single-add
	// degraded-embed behavior.
	if a.engine.Embedder() != nil && len(survivors) > 0 {
		texts := make([]string, len(survivors))
		for i, s := range survivors {
			texts[i] = s.vecText
		}
		vecs, err := a.engine.Embedder().Embed(ctx, texts)
		if err != nil {
			a.log.Warn("collection batch embed failed", "component", "collection",
				"n", len(survivors), "err", err)
		} else if len(vecs) == len(survivors) {
			for i := range survivors {
				if survivors[i].vecText != "" {
					survivors[i].vec = vecs[i]
				}
			}
		} else {
			a.log.Warn("collection batch embed returned wrong count",
				"component", "collection", "want", len(survivors), "got", len(vecs))
		}
	}

	// Phase 2: engine write lock. Collection existence, schema load,
	// dedup, and commit all happen under the same lock so nothing
	// races with a concurrent CollectionSchemaUpdate or a sibling
	// CollectionAdd with the same title.
	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return CollectionAddBatchResponse{}, svcErr
	}
	if isRetired(coll) {
		return CollectionAddBatchResponse{}, ErrInvalid("cannot add to retired collection")
	}
	schema, err := loadSchema(coll)
	if err != nil {
		a.log.Warn("collection schema load failed", "component", "collection", "err", err)
		return CollectionAddBatchResponse{}, ErrInternal("failed to load schema")
	}

	// Build the existing-title set once. Iterating collectionItemEdges
	// per item would be O(N*M); doing it once is O(M).
	existingTitles := make(map[string]string) // lowercased title -> existing item ID
	for _, e := range a.collectionItemEdges(collectionID) {
		n, ok := a.engine.Graph().GetNode(e.SourceID)
		if !ok {
			continue
		}
		if existing, ok := n.Properties.GetString("field.title"); ok {
			existingTitles[strings.ToLower(existing)] = e.SourceID
		}
	}

	added := make([]BatchAddSuccess, 0, len(survivors))
	emb := a.engine.Embedder()
	var modelID string
	if emb != nil {
		modelID = emb.ModelID()
	}

	// CollCache.AddMember opens its own bbolt write transaction, which
	// would deadlock if called inside the BatchIndexWrites closure
	// (which already holds the shared tx). Collect the IDs during the
	// closure and apply them after it returns.
	cachePending := make([]string, 0, len(survivors))

	batchErr := a.engine.BatchIndexWrites(func(ws *core.WriteSession) {
		for _, s := range survivors {
			// Per-item schema validation. Runs under lock so the
			// schema we validate against matches the schema we commit
			// under.
			if verr := validateItemFields(schema, s.item.Fields); verr != nil {
				failed = append(failed, BatchAddFailure{
					Index:     s.reqIdx,
					ClientRef: s.item.ClientRef,
					Code:      "input_error",
					Message:   verr.Error(),
				})
				continue
			}

			// Dedup pass: check against existing members AND against
			// prior items in this batch (first-write-wins).
			if title, ok := s.item.Fields["title"]; ok {
				if titleStr, isStr := title.(string); isStr && titleStr != "" {
					if existingID, dup := existingTitles[strings.ToLower(titleStr)]; dup {
						failed = append(failed, BatchAddFailure{
							Index:     s.reqIdx,
							ClientRef: s.item.ClientRef,
							Code:      "duplicate",
							Message:   fmt.Sprintf("item with title %q already exists in this collection (existing id: %s)", titleStr, existingID),
						})
						continue
					}
				}
			}

			// Create item node. Same shape as single CollectionAdd.
			props := graph.Properties{
				"created_at":   graph.TimestampProperty(time.Now().UTC()),
				"access_count": graph.Int64Property(0),
			}
			n := ws.AddNode(props)
			a.setFieldPropsIn(ws, n.ID, s.item.Fields)

			// Index + BM25 from string field values.
			var bm25Parts []string
			for _, v := range s.item.Fields {
				if str, ok := v.(string); ok {
					bm25Parts = append(bm25Parts, str)
				}
			}
			ws.IndexNode(n.ID, strings.Join(bm25Parts, " "), s.vec)
			if s.vec != nil && modelID != "" {
				ws.SetProp(n.ID, "embedding_model", graph.StringProperty(modelID))
			}

			if _, err := ws.AddEdge(n.ID, collectionID, "member_of", 1.0, nil); err != nil {
				a.log.Warn("collection batch member_of edge failed",
					"component", "collection", "collection_id", collectionID,
					"item_id", n.ID, "err", err)
				failed = append(failed, BatchAddFailure{
					Index:     s.reqIdx,
					ClientRef: s.item.ClientRef,
					Code:      "internal_error",
					Message:   "failed to add item to collection",
				})
				continue
			}
			cachePending = append(cachePending, n.ID)

			// Register intra-batch title so later items see this one
			// as a duplicate.
			if title, ok := s.item.Fields["title"]; ok {
				if titleStr, isStr := title.(string); isStr && titleStr != "" {
					existingTitles[strings.ToLower(titleStr)] = n.ID
				}
			}

			added = append(added, BatchAddSuccess{
				Index:     s.reqIdx,
				ClientRef: s.item.ClientRef,
				ID:        n.ID,
			})
		}
	})
	if batchErr != nil {
		a.log.Error("collection batch tx failed", "component", "collection",
			"collection_id", collectionID, "err", batchErr)
		return CollectionAddBatchResponse{}, ErrInternal("failed to commit batch")
	}

	// Apply queued member-cache updates outside the batch tx.
	if cc := a.engine.CollCache(); cc != nil {
		for _, id := range cachePending {
			cc.AddMember(collectionID, id)
		}
	}

	if _, err := a.engine.Save("collection_add_batch"); err != nil {
		a.log.Warn("collection batch save failed", "component", "collection", "err", err)
		return CollectionAddBatchResponse{}, ErrInternal("failed to save batch")
	}

	return CollectionAddBatchResponse{
		CollectionID: collectionID,
		Added:        added,
		Failed:       failed,
	}, nil
}

func (a *API) CollectionRemove(ctx context.Context, collectionID, itemID string) (map[string]any, *APIError) {
	_ = ctx
	a.engine.Lock()
	defer a.engine.Unlock()

	if _, svcErr := a.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}

	edge, ok := a.isMemberOf(itemID, collectionID)
	if !ok {
		return nil, ErrNotFound("item is not a member of this collection")
	}

	a.engine.Graph().DeleteEdge(edge.ID)
	if cc := a.engine.CollCache(); cc != nil {
		cc.RemoveMember(collectionID, itemID)
	}

	if _, err := a.engine.Save("collection_remove"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "remove", "err", err)
		return nil, ErrInternal("failed to save collection removal")
	}

	return map[string]any{"removed": true, "item_id": itemID, "collection_id": collectionID}, nil
}

type CollectionUpdateRequest struct {
	Fields map[string]any `json:"fields"`
}

func (a *API) CollectionUpdate(ctx context.Context, collectionID, itemID string, req *CollectionUpdateRequest) (map[string]any, *APIError) {
	_ = ctx
	if len(req.Fields) == 0 {
		return nil, ErrMissing("fields are required")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	if _, ok := a.isMemberOf(itemID, collectionID); !ok {
		return nil, ErrNotFound("item is not a member of this collection")
	}

	// Validate against schema. Merge existing fields with updates for
	// full validation (required fields might already be set).
	schema, err := loadSchema(coll)
	if err != nil {
		a.log.Warn("collection schema load failed", "component", "collection", "err", err)
		return nil, ErrInternal("failed to load schema")
	}
	if schema != nil {
		n, ok := a.engine.Graph().GetNode(itemID)
		if !ok {
			return nil, ErrNotFound("item not found")
		}
		merged := extractFields(n)
		for k, v := range req.Fields {
			merged[k] = v
		}
		if err := validateItemFields(schema, merged); err != nil {
			return nil, ErrInvalid(err.Error())
		}
	}

	a.setFieldProps(itemID, req.Fields)

	if _, err := a.engine.Save("collection_update"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "update", "err", err)
		return nil, ErrInternal("failed to save collection update")
	}

	return map[string]any{"updated": true, "item_id": itemID}, nil
}

type CollectionMoveRequest struct {
	TargetCollectionID string `json:"target_collection_id"`
}

func (a *API) CollectionMove(ctx context.Context, collectionID, itemID string, req *CollectionMoveRequest) (map[string]any, *APIError) {
	_ = ctx
	if req.TargetCollectionID == "" {
		return nil, ErrMissing("target_collection_id is required")
	}
	if req.TargetCollectionID == collectionID {
		return nil, ErrInvalid("target collection is the same as source")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	// Verify source.
	if _, svcErr := a.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}
	edge, ok := a.isMemberOf(itemID, collectionID)
	if !ok {
		return nil, ErrNotFound("item is not a member of source collection")
	}

	// Verify target.
	targetColl, svcErr := a.isCollection(req.TargetCollectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if isRetired(targetColl) {
		return nil, ErrInvalid("target collection is retired")
	}

	// Validate item fields against target schema.
	targetSchema, err := loadSchema(targetColl)
	if err != nil {
		a.log.Warn("target schema load failed", "component", "collection", "err", err)
		return nil, ErrInternal("failed to load target schema")
	}
	if targetSchema != nil {
		n, ok := a.engine.Graph().GetNode(itemID)
		if !ok {
			return nil, ErrNotFound("item not found")
		}
		fields := extractFields(n)
		if err := validateItemFields(targetSchema, fields); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("item does not satisfy target schema: %s", err))
		}
	}

	// Remove from source, add to target. Treat DeleteEdge ErrNotFound as
	// a benign race -- another request already moved or removed the
	// membership, so the source side is effectively empty. Any other
	// error must abort before AddEdge, otherwise the item would end up
	// in both collections.
	if err := a.engine.Graph().DeleteEdge(edge.ID); err != nil && !errors.Is(err, graph.ErrNotFound) {
		a.log.Warn("member_of delete failed", "component", "collection",
			"collection_id", collectionID, "item_id", itemID, "err", err)
		return nil, ErrInternal("failed to detach item from source collection")
	}
	if _, err := a.engine.Graph().AddEdge(itemID, req.TargetCollectionID, "member_of", 1.0, nil); err != nil {
		a.log.Warn("member_of add failed", "component", "collection",
			"collection_id", req.TargetCollectionID, "item_id", itemID, "err", err)
		if errors.Is(err, graph.ErrNotFound) {
			return nil, ErrNotFound("target collection or item missing during move")
		}
		return nil, ErrInternal("failed to add item to target collection")
	}
	if cc := a.engine.CollCache(); cc != nil {
		cc.RemoveMember(collectionID, itemID)
		cc.AddMember(req.TargetCollectionID, itemID)
	}

	if _, err := a.engine.Save("collection_move"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "move", "err", err)
		return nil, ErrInternal("failed to save collection move")
	}

	return map[string]any{
		"moved":                true,
		"item_id":              itemID,
		"from_collection_id":   collectionID,
		"to_collection_id":     req.TargetCollectionID,
	}, nil
}

type CollectionRenameRequest struct {
	Name string `json:"name"`
}

func (a *API) CollectionRename(ctx context.Context, collectionID string, req *CollectionRenameRequest) (map[string]any, *APIError) {
	_ = ctx
	if err := validateCollectionName(req.Name); err != nil {
		return nil, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, svcErr := a.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}

	// Check uniqueness (exclude self).
	if existingID, exists := a.collectionByName(req.Name); exists && existingID != collectionID {
		return nil, ErrConflict(fmt.Sprintf("collection %q already exists", req.Name))
	}

	a.engine.SetProp(collectionID, "collection_name", graph.StringProperty(req.Name))

	if _, err := a.engine.Save("collection_rename"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "rename", "err", err)
		return nil, ErrInternal("failed to save rename")
	}

	return map[string]any{"renamed": true, "id": collectionID, "name": req.Name}, nil
}

func (a *API) CollectionDelete(ctx context.Context, collectionID string) (map[string]any, *APIError) {
	_ = ctx
	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	if isRetired(coll) {
		// Already retired -- unretire.
		a.engine.Graph().RemoveNodeProperty(collectionID, "valid_until")
		if _, err := a.engine.Save("collection_unretire"); err != nil {
			a.log.Warn("collection save failed", "component", "collection", "op", "unretire", "err", err)
			return nil, ErrInternal("failed to save unretire")
		}
		return map[string]any{"unretired": true, "id": collectionID}, nil
	}

	// Retire: set valid_until, keep edges.
	a.engine.SetProp(collectionID, "valid_until", graph.TimestampProperty(time.Now().UTC()))

	if _, err := a.engine.Save("collection_retire"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "retire", "err", err)
		return nil, ErrInternal("failed to save retire")
	}

	itemCount := len(a.collectionItemEdges(collectionID))
	return map[string]any{"retired": true, "id": collectionID, "items_preserved": itemCount}, nil
}

func (a *API) CollectionSchemaRead(ctx context.Context, collectionID string) (map[string]any, *APIError) {
	_ = ctx
	a.engine.RLock()
	defer a.engine.RUnlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	schema, err := loadSchema(coll)
	if err != nil {
		a.log.Warn("collection schema load failed", "component", "collection", "err", err)
		return nil, ErrInternal("failed to load schema")
	}

	result := map[string]any{"collection_id": collectionID}
	if schema != nil {
		result["schema"] = schema
	}

	// Include migration state if active.
	if migFields, ok := coll.Properties.GetStringList("collection_migration_fields"); ok && len(migFields) > 0 {
		total, _ := coll.Properties.GetInt64("collection_migration_total")
		result["migration"] = map[string]any{
			"fields": migFields,
			"total":  total,
		}
	}

	return result, nil
}

type CollectionSchemaUpdateRequest struct {
	Schema CollectionSchema `json:"schema"`
}

func (a *API) CollectionSchemaUpdate(ctx context.Context, collectionID string, req *CollectionSchemaUpdateRequest) (map[string]any, *APIError) {
	_ = ctx
	if err := validateSchema(&req.Schema); err != nil {
		return nil, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	// Determine if new required fields were added.
	oldSchema, _ := loadSchema(coll)
	var newRequiredFields []string
	oldFields := make(map[string]bool)
	if oldSchema != nil {
		for _, f := range oldSchema.Fields {
			if f.Required {
				oldFields[f.Name] = true
			}
		}
	}
	for _, f := range req.Schema.Fields {
		if f.Required && !oldFields[f.Name] {
			newRequiredFields = append(newRequiredFields, f.Name)
		}
	}

	// Store updated schema.
	raw, err := serializeCollectionSchema(&req.Schema)
	if err != nil {
		a.log.Warn("collection schema serialize failed", "component", "collection", "err", err)
		return nil, ErrInternal("failed to serialize schema")
	}
	a.engine.SetProp(collectionID, "collection_schema", graph.StringProperty(raw))

	result := map[string]any{"updated": true, "collection_id": collectionID}

	// Enter migration state if new required fields.
	if len(newRequiredFields) > 0 {
		itemCount := len(a.collectionItemEdges(collectionID))
		a.engine.SetProp(collectionID, "collection_migration_fields",
			graph.StringListProperty(newRequiredFields))
		a.engine.SetProp(collectionID, "collection_migration_total",
			graph.Int64Property(int64(itemCount)))

		result["migration"] = map[string]any{
			"fields": newRequiredFields,
			"total":  itemCount,
			"message": fmt.Sprintf("%d items need migration for new required fields: %s",
				itemCount, strings.Join(newRequiredFields, ", ")),
		}
	}

	if _, err := a.engine.Save("collection_schema_update"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "schema_update", "err", err)
		return nil, ErrInternal("failed to save schema update")
	}

	return result, nil
}

type CollectionMigrateRequest struct {
	Field string `json:"field"`
	Value any    `json:"value"`
}

func (a *API) CollectionMigrate(ctx context.Context, collectionID string, req *CollectionMigrateRequest) (map[string]any, *APIError) {
	_ = ctx
	if req.Field == "" {
		return nil, ErrMissing("field is required")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	coll, svcErr := a.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	// Verify migration is active for this field.
	migFields, ok := coll.Properties.GetStringList("collection_migration_fields")
	if !ok || len(migFields) == 0 {
		return nil, ErrInvalid("no migration is active for this collection")
	}
	found := false
	for _, f := range migFields {
		if f == req.Field {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrInvalid(fmt.Sprintf("field %q is not in the migration set", req.Field))
	}

	// Validate the value against the schema if non-null.
	if req.Value != nil {
		schema, err := loadSchema(coll)
		if err != nil {
			a.log.Warn("collection schema load failed", "component", "collection", "err", err)
			return nil, ErrInternal("failed to load schema")
		}
		if schema != nil {
			for _, f := range schema.Fields {
				if f.Name == req.Field {
					if err := validateFieldValue(f, req.Value); err != nil {
						return nil, ErrInvalid(fmt.Sprintf("migration value for %q: %s", req.Field, err))
					}
					break
				}
			}
		}
	}

	// Bulk-update items missing the field.
	updated := 0
	for _, e := range a.collectionItemEdges(collectionID) {
		n, ok := a.engine.Graph().GetNode(e.SourceID)
		if !ok {
			continue
		}
		if _, has := n.Properties["field."+req.Field]; !has {
			a.setFieldProps(e.SourceID, map[string]any{req.Field: req.Value})
			updated++
		}
	}

	// Check if migration is complete for this field.
	remaining := make([]string, 0, len(migFields))
	for _, f := range migFields {
		if f == req.Field {
			continue // This field is done.
		}
		remaining = append(remaining, f)
	}

	if len(remaining) == 0 {
		// All migrations complete -- clear state.
		a.engine.Graph().RemoveNodeProperty(collectionID, "collection_migration_fields")
		a.engine.Graph().RemoveNodeProperty(collectionID, "collection_migration_total")
	} else {
		a.engine.SetProp(collectionID, "collection_migration_fields",
			graph.StringListProperty(remaining))
	}

	if _, err := a.engine.Save("collection_migrate"); err != nil {
		a.log.Warn("collection save failed", "component", "collection", "op", "migrate", "err", err)
		return nil, ErrInternal("failed to save migration")
	}

	return map[string]any{
		"migrated":          updated,
		"field":             req.Field,
		"migration_complete": len(remaining) == 0,
	}, nil
}

// compareAny provides basic ordering for sort. Handles string, float64, nil.
func compareAny(a, b any) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil {
		return true // nils sort first
	}
	if b == nil {
		return false
	}
	switch va := a.(type) {
	case string:
		vb, ok := b.(string)
		if !ok {
			return true
		}
		return va < vb
	case float64:
		vb, ok := b.(float64)
		if !ok {
			return true
		}
		return va < vb
	case json.Number:
		fa, _ := va.Float64()
		vb, ok := b.(json.Number)
		if !ok {
			return true
		}
		fb, _ := vb.Float64()
		return fa < fb
	default:
		return fmt.Sprint(a) < fmt.Sprint(b)
	}
}
