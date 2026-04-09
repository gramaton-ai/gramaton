package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// --- helpers ---

// collectionByName finds a collection by name (case-insensitive).
// Caller must hold at least RLock.
func (s *Server) collectionByName(name string) (string, bool) {
	ids := s.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("collection"))
	lower := strings.ToLower(name)
	for _, id := range ids {
		n, ok := s.engine.Graph().GetNode(id)
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
func (s *Server) collectionItemEdges(collectionID string) []*graph.Edge {
	var edges []*graph.Edge
	for _, e := range s.engine.Graph().EdgesTo(collectionID) {
		if e.Type == "member_of" {
			edges = append(edges, e)
		}
	}
	return edges
}

// isCollection checks if a node is a collection.
// Caller must hold at least RLock.
func (s *Server) isCollection(nodeID string) (*graph.Node, *serviceError) {
	n, ok := s.engine.Graph().GetNode(nodeID)
	if !ok {
		return nil, errNotFound("collection not found")
	}
	kt, _ := n.Properties.GetString("knowledge_type")
	if kt != "collection" {
		return nil, errNotFound("not a collection")
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
func (s *Server) setFieldProps(nodeID string, fields map[string]any) {
	for k, v := range fields {
		propKey := "field." + k
		switch val := v.(type) {
		case string:
			s.engine.SetProp(nodeID, propKey, graph.StringProperty(val))
		case float64:
			s.engine.SetProp(nodeID, propKey, graph.Float64Property(val))
		case bool:
			s.engine.SetProp(nodeID, propKey, graph.BoolProperty(val))
		case nil:
			// Explicit null -- remove the property if it exists.
			s.engine.SetProp(nodeID, propKey, graph.StringProperty(""))
		case []any:
			// enum[] -- store as StringList.
			ss := make([]string, len(val))
			for i, elem := range val {
				ss[i] = elem.(string) // validated by schema
			}
			s.engine.SetProp(nodeID, propKey, graph.StringListProperty(ss))
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
func (s *Server) isMemberOf(itemID, collectionID string) (*graph.Edge, bool) {
	for _, e := range s.engine.Graph().EdgesFrom(itemID) {
		if e.Type == "member_of" && e.TargetID == collectionID {
			return e, true
		}
	}
	return nil, false
}

// nodeCollectionNames returns the names of all collections a node belongs to.
// Caller must hold at least RLock.
func (s *Server) nodeCollectionNames(nodeID string) []string {
	var names []string
	for _, e := range s.engine.Graph().EdgesFrom(nodeID) {
		if e.Type != "member_of" {
			continue
		}
		n, ok := s.engine.Graph().GetNode(e.TargetID)
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

// --- service methods ---

type collectionCreateRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Schema      *CollectionSchema `json:"schema,omitempty"`
}

func (s *Server) serviceCollectionCreate(_ context.Context, req *collectionCreateRequest) (map[string]any, *serviceError) {
	if err := validateCollectionName(req.Name); err != nil {
		return nil, errInvalid(err.Error())
	}
	if err := validateSchema(req.Schema); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	// Enforce name uniqueness.
	if _, exists := s.collectionByName(req.Name); exists {
		return nil, errConflict(fmt.Sprintf("collection %q already exists", req.Name))
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
			return nil, errInternal(err.Error())
		}
		props["collection_schema"] = graph.StringProperty(raw)
	}

	n := s.engine.Graph().AddNode(props)
	bm25Text := req.Name
	if req.Description != "" {
		bm25Text += " " + req.Description
	}
	s.engine.IndexNode(n.ID, bm25Text, nil)

	if _, err := s.engine.Save("collection_create"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{"id": n.ID, "name": req.Name}, nil
}

type collectionListRequest struct {
	Limit  int
	Offset int
}

func (s *Server) serviceCollectionList(req *collectionListRequest) (map[string]any, *serviceError) {
	s.engine.RLock()
	defer s.engine.RUnlock()

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

	ids := s.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("collection"))
	all := make([]map[string]any, 0, len(ids))

	for _, id := range ids {
		n, ok := s.engine.Graph().GetNode(id)
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
		entry["item_count"] = len(s.collectionItemEdges(id))

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

type collectionItemsRequest struct {
	Sort           string `json:"sort,omitempty"`
	Order          string `json:"order,omitempty"`
	IncludeRetired bool   `json:"include_retired,omitempty"`
}

// serviceCollectionItems is deliberately unpaginated -- exhaustive retrieval is the
// contract that distinguishes collections from the knowledge graph. If a collection
// grows large enough to need pagination, it's a signal to split it.
func (s *Server) serviceCollectionItems(collectionID string, req *collectionItemsRequest) (map[string]any, *serviceError) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if !req.IncludeRetired && isRetired(coll) {
		return nil, errNotFound("collection is retired")
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

	edges := s.collectionItemEdges(collectionID)
	items := make([]map[string]any, 0, len(edges))

	for _, e := range edges {
		n, ok := s.engine.Graph().GetNode(e.SourceID)
		if !ok {
			continue
		}
		item := map[string]any{
			"id":     e.SourceID,
			"fields": extractFields(n),
		}
		if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
			item["created_at"] = ca.Format(time.RFC3339)
		}

		// Annotate pre-migration items.
		if migration != nil {
			migFields := migration["fields"].([]string)
			var missing []string
			for _, f := range migFields {
				if _, has := n.Properties["field."+f]; !has {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				item["needs_migration"] = missing
			}
		}

		items = append(items, item)
	}

	// Sort items.
	sortField := req.Sort
	descending := req.Order == "desc"
	if sortField == "" {
		sortField = "created_at"
	}
	sort.SliceStable(items, func(i, j int) bool {
		var vi, vj any
		if sortField == "created_at" {
			vi = items[i]["created_at"]
			vj = items[j]["created_at"]
		} else {
			fi := items[i]["fields"].(map[string]any)
			fj := items[j]["fields"].(map[string]any)
			vi = fi[sortField]
			vj = fj[sortField]
		}
		less := compareAny(vi, vj)
		if descending {
			return !less
		}
		return less
	})

	result := map[string]any{
		"collection_id": collectionID,
		"items":         items,
		"count":         len(items),
	}
	if migration != nil {
		done := 0
		for _, item := range items {
			if item["needs_migration"] == nil {
				done++
			}
		}
		migration["done"] = done
		migration["remaining"] = len(items) - done
		result["migration"] = migration
	}

	return result, nil
}

type collectionAddRequest struct {
	Fields map[string]any `json:"fields"`
}

func (s *Server) serviceCollectionAdd(collectionID string, req *collectionAddRequest) (map[string]any, *serviceError) {
	if len(req.Fields) == 0 {
		return nil, errMissing("fields are required")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if isRetired(coll) {
		return nil, errInvalid("cannot add to retired collection")
	}

	// Schema validation.
	schema, err := loadSchema(coll)
	if err != nil {
		return nil, errInternal(err.Error())
	}
	if err := validateItemFields(schema, req.Fields); err != nil {
		return nil, errInvalid(err.Error())
	}

	// Dedup check: look for existing item with same title.
	if title, ok := req.Fields["title"]; ok {
		titleStr, isStr := title.(string)
		if isStr && titleStr != "" {
			for _, e := range s.collectionItemEdges(collectionID) {
				n, ok := s.engine.Graph().GetNode(e.SourceID)
				if !ok {
					continue
				}
				if existing, ok := n.Properties.GetString("field.title"); ok {
					if strings.EqualFold(existing, titleStr) {
						return map[string]any{
							"duplicate": true,
							"existing_id": e.SourceID,
							"message": fmt.Sprintf("item with title %q already exists in this collection", titleStr),
						}, nil
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
	n := s.engine.Graph().AddNode(props)
	s.setFieldProps(n.ID, req.Fields)

	// Index for BM25 using field values.
	var bm25Parts []string
	for _, v := range req.Fields {
		if s, ok := v.(string); ok {
			bm25Parts = append(bm25Parts, s)
		}
	}
	s.engine.IndexNode(n.ID, strings.Join(bm25Parts, " "), nil)

	// Create member_of edge.
	if _, err := s.engine.Graph().AddEdge(n.ID, collectionID, "member_of", 1.0, nil); err != nil {
		return nil, errInternal(err.Error())
	}

	if _, err := s.engine.Save("collection_add"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{"id": n.ID, "collection_id": collectionID}, nil
}

func (s *Server) serviceCollectionRemove(collectionID, itemID string) (map[string]any, *serviceError) {
	s.engine.Lock()
	defer s.engine.Unlock()

	if _, svcErr := s.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}

	edge, ok := s.isMemberOf(itemID, collectionID)
	if !ok {
		return nil, errNotFound("item is not a member of this collection")
	}

	s.engine.Graph().DeleteEdge(edge.ID)

	if _, err := s.engine.Save("collection_remove"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{"removed": true, "item_id": itemID, "collection_id": collectionID}, nil
}

type collectionUpdateRequest struct {
	Fields map[string]any `json:"fields"`
}

func (s *Server) serviceCollectionUpdate(collectionID, itemID string, req *collectionUpdateRequest) (map[string]any, *serviceError) {
	if len(req.Fields) == 0 {
		return nil, errMissing("fields are required")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	if _, ok := s.isMemberOf(itemID, collectionID); !ok {
		return nil, errNotFound("item is not a member of this collection")
	}

	// Validate against schema. Merge existing fields with updates for
	// full validation (required fields might already be set).
	schema, err := loadSchema(coll)
	if err != nil {
		return nil, errInternal(err.Error())
	}
	if schema != nil {
		n, ok := s.engine.Graph().GetNode(itemID)
		if !ok {
			return nil, errNotFound("item not found")
		}
		merged := extractFields(n)
		for k, v := range req.Fields {
			merged[k] = v
		}
		if err := validateItemFields(schema, merged); err != nil {
			return nil, errInvalid(err.Error())
		}
	}

	s.setFieldProps(itemID, req.Fields)

	if _, err := s.engine.Save("collection_update"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{"updated": true, "item_id": itemID}, nil
}

type collectionMoveRequest struct {
	TargetCollectionID string `json:"target_collection_id"`
}

func (s *Server) serviceCollectionMove(collectionID, itemID string, req *collectionMoveRequest) (map[string]any, *serviceError) {
	if req.TargetCollectionID == "" {
		return nil, errMissing("target_collection_id is required")
	}
	if req.TargetCollectionID == collectionID {
		return nil, errInvalid("target collection is the same as source")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	// Verify source.
	if _, svcErr := s.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}
	edge, ok := s.isMemberOf(itemID, collectionID)
	if !ok {
		return nil, errNotFound("item is not a member of source collection")
	}

	// Verify target.
	targetColl, svcErr := s.isCollection(req.TargetCollectionID)
	if svcErr != nil {
		return nil, svcErr
	}
	if isRetired(targetColl) {
		return nil, errInvalid("target collection is retired")
	}

	// Validate item fields against target schema.
	targetSchema, err := loadSchema(targetColl)
	if err != nil {
		return nil, errInternal(err.Error())
	}
	if targetSchema != nil {
		n, ok := s.engine.Graph().GetNode(itemID)
		if !ok {
			return nil, errNotFound("item not found")
		}
		fields := extractFields(n)
		if err := validateItemFields(targetSchema, fields); err != nil {
			return nil, errInvalid(fmt.Sprintf("item does not satisfy target schema: %s", err))
		}
	}

	// Remove from source, add to target.
	s.engine.Graph().DeleteEdge(edge.ID)
	if _, err := s.engine.Graph().AddEdge(itemID, req.TargetCollectionID, "member_of", 1.0, nil); err != nil {
		return nil, errInternal(err.Error())
	}

	if _, err := s.engine.Save("collection_move"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{
		"moved":                true,
		"item_id":              itemID,
		"from_collection_id":   collectionID,
		"to_collection_id":     req.TargetCollectionID,
	}, nil
}

type collectionRenameRequest struct {
	Name string `json:"name"`
}

func (s *Server) serviceCollectionRename(collectionID string, req *collectionRenameRequest) (map[string]any, *serviceError) {
	if err := validateCollectionName(req.Name); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, svcErr := s.isCollection(collectionID); svcErr != nil {
		return nil, svcErr
	}

	// Check uniqueness (exclude self).
	if existingID, exists := s.collectionByName(req.Name); exists && existingID != collectionID {
		return nil, errConflict(fmt.Sprintf("collection %q already exists", req.Name))
	}

	s.engine.SetProp(collectionID, "collection_name", graph.StringProperty(req.Name))

	if _, err := s.engine.Save("collection_rename"); err != nil {
		return nil, errInternal(err.Error())
	}

	return map[string]any{"renamed": true, "id": collectionID, "name": req.Name}, nil
}

func (s *Server) serviceCollectionDelete(collectionID string) (map[string]any, *serviceError) {
	s.engine.Lock()
	defer s.engine.Unlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	if isRetired(coll) {
		// Already retired -- unretire.
		s.engine.Graph().RemoveNodeProperty(collectionID, "valid_until")
		if _, err := s.engine.Save("collection_unretire"); err != nil {
			return nil, errInternal(err.Error())
		}
		return map[string]any{"unretired": true, "id": collectionID}, nil
	}

	// Retire: set valid_until, keep edges.
	s.engine.SetProp(collectionID, "valid_until", graph.TimestampProperty(time.Now().UTC()))

	if _, err := s.engine.Save("collection_retire"); err != nil {
		return nil, errInternal(err.Error())
	}

	itemCount := len(s.collectionItemEdges(collectionID))
	return map[string]any{"retired": true, "id": collectionID, "items_preserved": itemCount}, nil
}

func (s *Server) serviceCollectionSchemaRead(collectionID string) (map[string]any, *serviceError) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	schema, err := loadSchema(coll)
	if err != nil {
		return nil, errInternal(err.Error())
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

type collectionSchemaUpdateRequest struct {
	Schema CollectionSchema `json:"schema"`
}

func (s *Server) serviceCollectionSchemaUpdate(collectionID string, req *collectionSchemaUpdateRequest) (map[string]any, *serviceError) {
	if err := validateSchema(&req.Schema); err != nil {
		return nil, errInvalid(err.Error())
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	coll, svcErr := s.isCollection(collectionID)
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
		return nil, errInternal(err.Error())
	}
	s.engine.SetProp(collectionID, "collection_schema", graph.StringProperty(raw))

	result := map[string]any{"updated": true, "collection_id": collectionID}

	// Enter migration state if new required fields.
	if len(newRequiredFields) > 0 {
		itemCount := len(s.collectionItemEdges(collectionID))
		s.engine.SetProp(collectionID, "collection_migration_fields",
			graph.StringListProperty(newRequiredFields))
		s.engine.SetProp(collectionID, "collection_migration_total",
			graph.Int64Property(int64(itemCount)))

		result["migration"] = map[string]any{
			"fields": newRequiredFields,
			"total":  itemCount,
			"message": fmt.Sprintf("%d items need migration for new required fields: %s",
				itemCount, strings.Join(newRequiredFields, ", ")),
		}
	}

	if _, err := s.engine.Save("collection_schema_update"); err != nil {
		return nil, errInternal(err.Error())
	}

	return result, nil
}

type collectionMigrateRequest struct {
	Field string `json:"field"`
	Value any    `json:"value"`
}

func (s *Server) serviceCollectionMigrate(collectionID string, req *collectionMigrateRequest) (map[string]any, *serviceError) {
	if req.Field == "" {
		return nil, errMissing("field is required")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	coll, svcErr := s.isCollection(collectionID)
	if svcErr != nil {
		return nil, svcErr
	}

	// Verify migration is active for this field.
	migFields, ok := coll.Properties.GetStringList("collection_migration_fields")
	if !ok || len(migFields) == 0 {
		return nil, errInvalid("no migration is active for this collection")
	}
	found := false
	for _, f := range migFields {
		if f == req.Field {
			found = true
			break
		}
	}
	if !found {
		return nil, errInvalid(fmt.Sprintf("field %q is not in the migration set", req.Field))
	}

	// Validate the value against the schema if non-null.
	if req.Value != nil {
		schema, err := loadSchema(coll)
		if err != nil {
			return nil, errInternal(err.Error())
		}
		if schema != nil {
			for _, f := range schema.Fields {
				if f.Name == req.Field {
					if err := validateFieldValue(f, req.Value); err != nil {
						return nil, errInvalid(fmt.Sprintf("migration value for %q: %s", req.Field, err))
					}
					break
				}
			}
		}
	}

	// Bulk-update items missing the field.
	updated := 0
	for _, e := range s.collectionItemEdges(collectionID) {
		n, ok := s.engine.Graph().GetNode(e.SourceID)
		if !ok {
			continue
		}
		if _, has := n.Properties["field."+req.Field]; !has {
			s.setFieldProps(e.SourceID, map[string]any{req.Field: req.Value})
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
		s.engine.Graph().RemoveNodeProperty(collectionID, "collection_migration_fields")
		s.engine.Graph().RemoveNodeProperty(collectionID, "collection_migration_total")
	} else {
		s.engine.SetProp(collectionID, "collection_migration_fields",
			graph.StringListProperty(remaining))
	}

	if _, err := s.engine.Save("collection_migrate"); err != nil {
		return nil, errInternal(err.Error())
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
		fb, _ := b.(json.Number).Float64()
		return fa < fb
	default:
		return fmt.Sprint(a) < fmt.Sprint(b)
	}
}
