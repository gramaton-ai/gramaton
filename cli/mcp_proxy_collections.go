package cli

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCollectionProxyTools(mcpServer *mcp.Server) {
	registerCollectionCreateProxy(mcpServer)
	registerCollectionListProxy(mcpServer)
	registerCollectionItemsProxy(mcpServer)
	registerCollectionAddProxy(mcpServer)
	registerCollectionUpdateProxy(mcpServer)
	registerCollectionMoveProxy(mcpServer)
	registerCollectionRemoveProxy(mcpServer)
	registerCollectionRenameProxy(mcpServer)
	registerCollectionDeleteProxy(mcpServer)
	registerCollectionSchemaProxy(mcpServer)
	registerCollectionMigrateProxy(mcpServer)
}

// --- create ---

type proxyCollectionCreateInput struct {
	Name        string `json:"name" jsonschema:"collection name (unique within store, max 128 chars)"`
	Description string `json:"description,omitempty" jsonschema:"optional description"`
	Schema      any    `json:"schema,omitempty" jsonschema:"optional schema defining item fields"`
}

func registerCollectionCreateProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_create",
		Description: "Create a new collection. Collections provide structured, exhaustive retrieval -- every item is always returned. Use for tasks, backlogs, reading lists, checklists. Use the knowledge graph (gramaton_capture) for semantic knowledge like decisions, context, and research.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionCreateInput) (*mcp.CallToolResult, any, error) {
		if args.Name == "" {
			return proxyErr("name is required")
		}
		return proxyPost("/v1/collections", args)
	})
}

// --- list ---

type proxyCollectionListInput struct {
	Limit  int `json:"limit,omitempty" jsonschema:"max collections to return (default 50, max 500)"`
	Offset int `json:"offset,omitempty" jsonschema:"starting position for pagination (default 0)"`
}

func registerCollectionListProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_list",
		Description: "List collections with names, item counts, and schema status. Returns {showing, total, has_more, next_offset} for pagination. Call again with offset=next_offset to get the next page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionListInput) (*mcp.CallToolResult, any, error) {
		path := "/v1/collections"
		params := url.Values{}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		if args.Offset > 0 {
			params.Set("offset", fmt.Sprintf("%d", args.Offset))
		}
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}

// --- items ---

type proxyCollectionItemsInput struct {
	CollectionID   string `json:"collection_id" jsonschema:"collection ID"`
	Sort           string `json:"sort,omitempty" jsonschema:"field name to sort by (default: created_at)"`
	Order          string `json:"order,omitempty" jsonschema:"asc or desc (default: asc)"`
	IncludeRetired bool   `json:"include_retired,omitempty" jsonschema:"include items from retired collections"`
}

func registerCollectionItemsProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_items",
		Description: "List ALL items in a collection. Returns every item, guaranteed complete. Supports sorting by any field.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionItemsInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items", url.PathEscape(args.CollectionID))
		params := url.Values{}
		if args.Sort != "" {
			params.Set("sort", args.Sort)
		}
		if args.Order != "" {
			params.Set("order", args.Order)
		}
		if args.IncludeRetired {
			params.Set("include_retired", "true")
		}
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}

// --- add ---

type proxyCollectionAddInput struct {
	CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
	Fields       map[string]any `json:"fields" jsonschema:"item fields (must match collection schema if defined)"`
}

func registerCollectionAddProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_add",
		Description: "Add an item to a collection. Use for tasks, TODOs, action items, or any structured data that needs exhaustive tracking. Fields are validated against the collection's schema. Returns duplicate info if an item with the same title already exists.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionAddInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items", url.PathEscape(args.CollectionID))
		return proxyPost(path, map[string]any{"fields": args.Fields})
	})
}

// --- update ---

type proxyCollectionUpdateInput struct {
	CollectionID string         `json:"collection_id" jsonschema:"collection ID"`
	ItemID       string         `json:"item_id" jsonschema:"item ID to update"`
	Fields       map[string]any `json:"fields" jsonschema:"fields to update (merged with existing, validated against schema)"`
}

func registerCollectionUpdateProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_update",
		Description: "Update fields on a collection item. Existing fields are preserved; only specified fields are changed. Validated against the collection schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionUpdateInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.ItemID == "" {
			return proxyErr("collection_id and item_id are required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items/%s",
			url.PathEscape(args.CollectionID), url.PathEscape(args.ItemID))
		return proxyPatch(path, map[string]any{"fields": args.Fields})
	})
}

// --- move ---

type proxyCollectionMoveInput struct {
	CollectionID       string `json:"collection_id" jsonschema:"source collection ID"`
	ItemID             string `json:"item_id" jsonschema:"item ID to move"`
	TargetCollectionID string `json:"target_collection_id" jsonschema:"destination collection ID"`
}

func registerCollectionMoveProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_move",
		Description: "Move an item from one collection to another. The item's fields are validated against the target collection's schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionMoveInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.ItemID == "" || args.TargetCollectionID == "" {
			return proxyErr("collection_id, item_id, and target_collection_id are required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items/%s/move",
			url.PathEscape(args.CollectionID), url.PathEscape(args.ItemID))
		return proxyPost(path, map[string]any{"target_collection_id": args.TargetCollectionID})
	})
}

// --- remove ---

type proxyCollectionRemoveInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection ID"`
	ItemID       string `json:"item_id" jsonschema:"item ID to remove"`
}

func registerCollectionRemoveProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_remove",
		Description: "Remove an item from a collection. The item node is preserved in the graph; only the membership edge is deleted.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionRemoveInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.ItemID == "" {
			return proxyErr("collection_id and item_id are required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items/%s",
			url.PathEscape(args.CollectionID), url.PathEscape(args.ItemID))
		return proxyDelete(path)
	})
}

// --- rename ---

type proxyCollectionRenameInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection ID"`
	Name         string `json:"name" jsonschema:"new name (must be unique within store)"`
}

func registerCollectionRenameProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_rename",
		Description: "Rename a collection. Name must be unique.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionRenameInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.Name == "" {
			return proxyErr("collection_id and name are required")
		}
		path := fmt.Sprintf("/v1/collections/%s", url.PathEscape(args.CollectionID))
		return proxyPatch(path, map[string]any{"name": args.Name})
	})
}

// --- delete (retire/unretire) ---

type proxyCollectionDeleteInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection ID to retire (or unretire if already retired)"`
}

func registerCollectionDeleteProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_delete",
		Description: "Retire a collection (reversible). Items and edges are preserved. Call again on a retired collection to re-activate it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionDeleteInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		path := fmt.Sprintf("/v1/collections/%s", url.PathEscape(args.CollectionID))
		return proxyDelete(path)
	})
}

// --- schema ---

type proxyCollectionSchemaInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection ID"`
}

func registerCollectionSchemaProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_schema",
		Description: "Read a collection's schema and migration status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionSchemaInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		path := fmt.Sprintf("/v1/collections/%s/schema", url.PathEscape(args.CollectionID))
		return proxyGet(path)
	})
}

// --- migrate ---

type proxyCollectionMigrateInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection ID"`
	Field        string `json:"field" jsonschema:"field name to migrate"`
	Value        any    `json:"value" jsonschema:"value to set on all items missing this field (use null for explicit null)"`
}

func registerCollectionMigrateProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_migrate",
		Description: "Bulk-update items for a schema migration. Sets the specified field on all items that are missing it. Required after adding a new required field to a schema.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionMigrateInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.Field == "" {
			return proxyErr("collection_id and field are required")
		}
		path := fmt.Sprintf("/v1/collections/%s/migrate", url.PathEscape(args.CollectionID))
		return proxyPost(path, map[string]any{"field": args.Field, "value": args.Value})
	})
}
