package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCollectionsRoutes wires collection HTTP endpoints to the api.
func (s *Server) registerCollectionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/collections", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionCreateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionCreate(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, result)
	})

	mux.HandleFunc("GET /v1/collections", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CollectionList(r.Context(), api.CollectionListRequest{
			Limit:  parseIntParam(r, "limit", 50, 500),
			Offset: parseIntParam(r, "offset", 0, 100000),
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /v1/collections/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		// fields=title,status (comma-separated) or repeated ?fields=...
		var projection []string
		for _, raw := range query["fields"] {
			for _, part := range strings.Split(raw, ",") {
				if part = strings.TrimSpace(part); part != "" {
					projection = append(projection, part)
				}
			}
		}
		// filter.<key>=val[,val2] -- comma = any-of
		var filter map[string]any
		for key, values := range query {
			if !strings.HasPrefix(key, "filter.") {
				continue
			}
			field := strings.TrimPrefix(key, "filter.")
			if field == "" {
				continue
			}
			var allowed []string
			for _, v := range values {
				for _, part := range strings.Split(v, ",") {
					if part = strings.TrimSpace(part); part != "" {
						allowed = append(allowed, part)
					}
				}
			}
			if len(allowed) == 0 {
				continue
			}
			if filter == nil {
				filter = make(map[string]any, 4)
			}
			if len(allowed) == 1 {
				filter[field] = allowed[0]
			} else {
				filter[field] = allowed
			}
		}
		result, apiErr := s.api.CollectionItems(r.Context(), r.PathValue("id"), api.CollectionItemsRequest{
			Sort:           query.Get("sort"),
			Order:          query.Get("order"),
			IncludeRetired: query.Get("include_retired") == "true",
			Fields:         projection,
			Filter:         filter,
			AsOf:           query.Get("as_of"),
		})
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/collections/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionAddRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionAdd(r.Context(), r.PathValue("id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, result)
	})

	mux.HandleFunc("POST /v1/collections/{id}/items/batch", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionAddBatchRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionAddBatch(r.Context(), r.PathValue("id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("PATCH /v1/collections/{id}/items/{item_id}", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionUpdateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionUpdate(r.Context(), r.PathValue("id"), r.PathValue("item_id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/collections/{id}/items/{item_id}/move", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionMoveRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionMove(r.Context(), r.PathValue("id"), r.PathValue("item_id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("DELETE /v1/collections/{id}/items/{item_id}", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CollectionRemove(r.Context(), r.PathValue("id"), r.PathValue("item_id"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("PATCH /v1/collections/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionRenameRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionRename(r.Context(), r.PathValue("id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("DELETE /v1/collections/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CollectionDelete(r.Context(), r.PathValue("id"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /v1/collections/{id}/schema", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.CollectionSchemaRead(r.Context(), r.PathValue("id"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("PUT /v1/collections/{id}/schema", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionSchemaUpdateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionSchemaUpdate(r.Context(), r.PathValue("id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/collections/{id}/migrate", func(w http.ResponseWriter, r *http.Request) {
		var req api.CollectionMigrateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CollectionMigrate(r.Context(), r.PathValue("id"), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})
}

// registerCollectionsMCPTools wires the collections cluster MCP tools.
func (s *Server) registerCollectionsMCPTools(mcpServer *mcp.Server) {
	type createArgs struct {
		Name           string                `json:"name" jsonschema:"collection name (unique within store, max 128 chars)"`
		Description    string                `json:"description,omitempty" jsonschema:"optional description"`
		Schema         *api.CollectionSchema `json:"schema,omitempty" jsonschema:"optional schema defining item fields"`
		ClearMode      string                `json:"clear_mode,omitempty" jsonschema:"how items are cleared when the collection is cleared: resolve (default, sets resolution=completed + valid_until) or unlink (remove member_of edge, keep item record)"`
		Supersession   string                `json:"supersession,omitempty" jsonschema:"auto-supersession candidate scope: collection (default, only same-collection records), store (legacy store-wide), or none (opt out entirely)"`
		Curation       string                `json:"curation,omitempty" jsonschema:"LLM analysis intensity: standard (default, runs classify/summarize/observation_extract/concept synthesis) or none (skip all LLM stages; embed + supersession + contradictions still governed by their own knobs)"`
		Contradictions string                `json:"contradictions,omitempty" jsonschema:"whether the system generates contradicts edges from records in this collection: on (default) or off"`
		Template       string                `json:"template,omitempty" jsonschema:"optional template name (backlog, todo, reading-list, shopping-list, packing-list). Applies template defaults for schema + behaviour knobs; caller-provided fields override."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_create",
		Description: api.CollectionCreateDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_create")
		defer done(nil)
		result, apiErr := s.api.CollectionCreate(ctx, api.CollectionCreateRequest{
			Name:           args.Name,
			Description:    args.Description,
			Schema:         args.Schema,
			ClearMode:      args.ClearMode,
			Supersession:   args.Supersession,
			Curation:       args.Curation,
			Contradictions: args.Contradictions,
			Template:       args.Template,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type listArgs struct {
		Limit  int `json:"limit,omitempty" jsonschema:"max collections to return (default 50, max 500)"`
		Offset int `json:"offset,omitempty" jsonschema:"starting position for pagination (default 0)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_list",
		Description: api.CollectionListDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_list")
		defer done(nil)
		result, apiErr := s.api.CollectionList(ctx, api.CollectionListRequest{Limit: args.Limit, Offset: args.Offset})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type itemsArgs struct {
		CollectionID   string         `json:"collection_id" jsonschema:"collection ID"`
		Sort           string         `json:"sort,omitempty" jsonschema:"field name to sort by (default: created_at)"`
		Order          string         `json:"order,omitempty" jsonschema:"asc or desc (default: asc)"`
		IncludeRetired bool           `json:"include_retired,omitempty" jsonschema:"include items from retired collections"`
		Fields         []string       `json:"fields,omitempty" jsonschema:"allowlist of schema field names to include per item (default: all fields). id, created_at, and needs_migration are always included."`
		Filter         map[string]any `json:"filter,omitempty" jsonschema:"schema-field -> expected-value(s) map. Value may be a string (exact match) or []string (any-of). Items must match every entry. Useful for auditing status=open or severity=P1 without dragging the full details payload."`
		AsOf           string         `json:"as_of,omitempty" jsonschema:"point-in-time membership: return members the collection had at the commit at-or-before this date (YYYY-MM-DD or RFC3339). Response carries as_of + semantics=point_in_time. Future dates rejected."`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_items",
		Description: api.CollectionItemsDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args itemsArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_items")
		defer done(nil)
		result, apiErr := s.api.CollectionItems(ctx, args.CollectionID, api.CollectionItemsRequest{
			Sort:           args.Sort,
			Order:          args.Order,
			IncludeRetired: args.IncludeRetired,
			Fields:         args.Fields,
			Filter:         args.Filter,
			AsOf:           args.AsOf,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type addArgs struct {
		CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
		Fields       map[string]any `json:"fields" jsonschema:"item fields (must match collection schema if defined)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_add",
		Description: api.CollectionAddDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_add")
		defer done(nil)
		result, apiErr := s.api.CollectionAdd(ctx, args.CollectionID, api.CollectionAddRequest{Fields: args.Fields})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type addBatchArgs struct {
		CollectionID string                  `json:"collection_id" jsonschema:"collection ID"`
		Items        []api.CollectionAddItem `json:"items" jsonschema:"array of items to add (max 500)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_add_batch",
		Description: api.CollectionAddBatchDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addBatchArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_add_batch")
		defer done(nil)
		result, apiErr := s.api.CollectionAddBatch(ctx, args.CollectionID, api.CollectionAddBatchRequest{Items: args.Items})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type updateArgs struct {
		CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
		ItemID       string         `json:"item_id" jsonschema:"item ID to update"`
		Fields       map[string]any `json:"fields" jsonschema:"fields to update (merged with existing, validated against schema)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_update",
		Description: api.CollectionUpdateDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_update")
		defer done(nil)
		result, apiErr := s.api.CollectionUpdate(ctx, args.CollectionID, args.ItemID, api.CollectionUpdateRequest{Fields: args.Fields})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type moveArgs struct {
		CollectionID       string `json:"collection_id" jsonschema:"source collection ID"`
		ItemID             string `json:"item_id" jsonschema:"item ID to move"`
		TargetCollectionID string `json:"target_collection_id" jsonschema:"destination collection ID"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_move",
		Description: api.CollectionMoveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args moveArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_move")
		defer done(nil)
		result, apiErr := s.api.CollectionMove(ctx, args.CollectionID, args.ItemID, api.CollectionMoveRequest{TargetCollectionID: args.TargetCollectionID})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type removeArgs struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		ItemID       string `json:"item_id" jsonschema:"item ID to remove"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_remove",
		Description: api.CollectionRemoveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args removeArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_remove")
		defer done(nil)
		result, apiErr := s.api.CollectionRemove(ctx, args.CollectionID, args.ItemID)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type renameArgs struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		Name         string `json:"name" jsonschema:"new name (must be unique within store)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_rename",
		Description: api.CollectionRenameDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args renameArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_rename")
		defer done(nil)
		result, apiErr := s.api.CollectionRename(ctx, args.CollectionID, api.CollectionRenameRequest{Name: args.Name})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type deleteArgs struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID to retire (or unretire if already retired)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_delete",
		Description: api.CollectionDeleteDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args deleteArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_delete")
		defer done(nil)
		result, apiErr := s.api.CollectionDelete(ctx, args.CollectionID)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type schemaArgs struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_schema",
		Description: api.CollectionSchemaDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args schemaArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_schema")
		defer done(nil)
		result, apiErr := s.api.CollectionSchemaRead(ctx, args.CollectionID)
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})

	type migrateArgs struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		Field        string `json:"field" jsonschema:"field name to migrate"`
		Value        any    `json:"value" jsonschema:"value to set on all items missing this field (use null for explicit null)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_migrate",
		Description: api.CollectionMigrateDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args migrateArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_migrate")
		defer done(nil)
		result, apiErr := s.api.CollectionMigrate(ctx, args.CollectionID, api.CollectionMigrateRequest{
			Field: args.Field, Value: args.Value,
		})
		if apiErr != nil {
			return mcpAPIErr(apiErr)
		}
		return mcpJSONResult(result)
	})
}
