package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPCollectionTools(mcpServer *mcp.Server) {
	// --- create ---

	type createInput struct {
		Name        string            `json:"name" jsonschema:"collection name (unique within store, max 128 chars)"`
		Description string            `json:"description,omitempty" jsonschema:"optional description"`
		Schema      *CollectionSchema `json:"schema,omitempty" jsonschema:"optional schema defining item fields"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_create",
		Description: "Create a new collection. Collections provide structured, exhaustive retrieval -- every item is always returned. Use for tasks, backlogs, reading lists, checklists. Use the knowledge graph (gramaton_capture) for semantic knowledge like decisions, context, and research.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args createInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_create")
		defer done(nil)
		result, svcErr := s.serviceCollectionCreate(ctx, &collectionCreateRequest{
			Name:        args.Name,
			Description: args.Description,
			Schema:      args.Schema,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- list ---

	type listInput struct {
		Limit  int `json:"limit,omitempty" jsonschema:"max collections to return (default 50, max 500)"`
		Offset int `json:"offset,omitempty" jsonschema:"starting position for pagination (default 0)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_list",
		Description: "List collections with names, item counts, and schema status. Returns {showing, total, has_more, next_offset} for pagination. Call again with offset=next_offset to get the next page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_list")
		defer done(nil)
		result, svcErr := s.serviceCollectionList(&collectionListRequest{
			Limit:  args.Limit,
			Offset: args.Offset,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- items ---

	type itemsInput struct {
		CollectionID   string `json:"collection_id" jsonschema:"collection ID"`
		Sort           string `json:"sort,omitempty" jsonschema:"field name to sort by (default: created_at)"`
		Order          string `json:"order,omitempty" jsonschema:"asc or desc (default: asc)"`
		IncludeRetired bool   `json:"include_retired,omitempty" jsonschema:"include items from retired collections"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_items",
		Description: "List ALL items in a collection. Returns every item, guaranteed complete. Supports sorting by any field.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args itemsInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_items")
		defer done(nil)
		result, svcErr := s.serviceCollectionItems(args.CollectionID, &collectionItemsRequest{
			Sort:           args.Sort,
			Order:          args.Order,
			IncludeRetired: args.IncludeRetired,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- add ---

	type addInput struct {
		CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
		Fields       map[string]any `json:"fields" jsonschema:"item fields (must match collection schema if defined)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_add",
		Description: "Add an item to a collection. Use for tasks, TODOs, action items, or any structured data that needs exhaustive tracking. Fields are validated against the collection's schema. Returns duplicate info if an item with the same title already exists.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args addInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_add")
		defer done(nil)
		result, svcErr := s.serviceCollectionAdd(args.CollectionID, &collectionAddRequest{
			Fields: args.Fields,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- update ---

	type updateInput struct {
		CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
		ItemID       string         `json:"item_id" jsonschema:"item ID to update"`
		Fields       map[string]any `json:"fields" jsonschema:"fields to update (merged with existing, validated against schema)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_update",
		Description: "Update fields on a collection item. Existing fields are preserved; only specified fields are changed. Validated against the collection schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_update")
		defer done(nil)
		result, svcErr := s.serviceCollectionUpdate(args.CollectionID, args.ItemID, &collectionUpdateRequest{
			Fields: args.Fields,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- move ---

	type moveInput struct {
		CollectionID       string `json:"collection_id" jsonschema:"source collection ID"`
		ItemID             string `json:"item_id" jsonschema:"item ID to move"`
		TargetCollectionID string `json:"target_collection_id" jsonschema:"destination collection ID"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_move",
		Description: "Move an item from one collection to another. The item's fields are validated against the target collection's schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args moveInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_move")
		defer done(nil)
		result, svcErr := s.serviceCollectionMove(args.CollectionID, args.ItemID, &collectionMoveRequest{
			TargetCollectionID: args.TargetCollectionID,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- remove ---

	type removeInput struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		ItemID       string `json:"item_id" jsonschema:"item ID to remove"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_remove",
		Description: "Remove an item from a collection. The item node is preserved in the graph; only the membership edge is deleted.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args removeInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_remove")
		defer done(nil)
		result, svcErr := s.serviceCollectionRemove(args.CollectionID, args.ItemID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- rename ---

	type renameInput struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		Name         string `json:"name" jsonschema:"new name (must be unique within store)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_rename",
		Description: "Rename a collection. Name must be unique.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args renameInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_rename")
		defer done(nil)
		result, svcErr := s.serviceCollectionRename(args.CollectionID, &collectionRenameRequest{
			Name: args.Name,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- delete (retire/unretire) ---

	type deleteInput struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID to retire (or unretire if already retired)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_delete",
		Description: "Retire a collection (reversible). Items and edges are preserved. Call again on a retired collection to re-activate it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args deleteInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_delete")
		defer done(nil)
		result, svcErr := s.serviceCollectionDelete(args.CollectionID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- schema ---

	type schemaReadInput struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_schema",
		Description: "Read a collection's schema and migration status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args schemaReadInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_schema")
		defer done(nil)
		result, svcErr := s.serviceCollectionSchemaRead(args.CollectionID)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	// --- migrate ---

	type migrateInput struct {
		CollectionID string `json:"collection_id" jsonschema:"collection ID"`
		Field        string `json:"field" jsonschema:"field name to migrate"`
		Value        any    `json:"value" jsonschema:"value to set on all items missing this field (use null for explicit null)"`
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_collection_migrate",
		Description: "Bulk-update items for a schema migration. Sets the specified field on all items that are missing it. Required after adding a new required field to a schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args migrateInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_collection_migrate")
		defer done(nil)
		result, svcErr := s.serviceCollectionMigrate(args.CollectionID, &collectionMigrateRequest{
			Field: args.Field,
			Value: args.Value,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
