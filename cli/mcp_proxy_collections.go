package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCollectionProxyTools(mcpServer *mcp.Server) {
	registerCollectionCreateProxy(mcpServer)
	registerCollectionListProxy(mcpServer)
	registerCollectionItemsProxy(mcpServer)
	registerCollectionAddProxy(mcpServer)
	registerCollectionAddBatchProxy(mcpServer)
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
		Description: api.CollectionCreateDescription,
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
		Description: api.CollectionListDescription,
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
	CollectionID   string         `json:"collection_id" jsonschema:"collection ID"`
	Sort           string         `json:"sort,omitempty" jsonschema:"field name to sort by (default: created_at)"`
	Order          string         `json:"order,omitempty" jsonschema:"asc or desc (default: asc)"`
	IncludeRetired bool           `json:"include_retired,omitempty" jsonschema:"include items from retired collections"`
	Fields         []string       `json:"fields,omitempty" jsonschema:"allowlist of schema field names to include per item (default: all fields). id, created_at, and needs_migration are always included."`
	Filter         map[string]any `json:"filter,omitempty" jsonschema:"schema-field -> expected-value(s) map. Value may be a string (exact match) or []string (any-of). Items must match every entry."`
}

func registerCollectionItemsProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_items",
		Description: api.CollectionItemsDescription,
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
		for _, f := range args.Fields {
			if f = strings.TrimSpace(f); f != "" {
				params.Add("fields", f)
			}
		}
		for key, raw := range args.Filter {
			if key == "" {
				continue
			}
			switch v := raw.(type) {
			case string:
				if v != "" {
					params.Set("filter."+key, v)
				}
			case []string:
				if len(v) > 0 {
					params.Set("filter."+key, strings.Join(v, ","))
				}
			case []any:
				parts := make([]string, 0, len(v))
				for _, elem := range v {
					if s, ok := elem.(string); ok && s != "" {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					params.Set("filter."+key, strings.Join(parts, ","))
				}
			}
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
		Description: api.CollectionAddDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionAddInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items", url.PathEscape(args.CollectionID))
		return proxyPost(path, map[string]any{"fields": args.Fields})
	})
}

// --- add batch ---

type proxyCollectionAddBatchInput struct {
	CollectionID string                  `json:"collection_id" jsonschema:"collection ID"`
	Items        []api.CollectionAddItem `json:"items" jsonschema:"array of items to add (max 500)"`
}

func registerCollectionAddBatchProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_collection_add_batch",
		Description: api.CollectionAddBatchDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionAddBatchInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" {
			return proxyErr("collection_id is required")
		}
		if len(args.Items) == 0 {
			return proxyErr("items is required")
		}
		path := fmt.Sprintf("/v1/collections/%s/items/batch", url.PathEscape(args.CollectionID))
		return proxyPost(path, map[string]any{"items": args.Items})
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
		Description: api.CollectionUpdateDescription,
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
		Description: api.CollectionMoveDescription,
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
		Description: api.CollectionRemoveDescription,
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
		Description: api.CollectionRenameDescription,
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
		Description: api.CollectionDeleteDescription,
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
		Description: api.CollectionSchemaDescription,
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
		Description: api.CollectionMigrateDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCollectionMigrateInput) (*mcp.CallToolResult, any, error) {
		if args.CollectionID == "" || args.Field == "" {
			return proxyErr("collection_id and field are required")
		}
		path := fmt.Sprintf("/v1/collections/%s/migrate", url.PathEscape(args.CollectionID))
		return proxyPost(path, map[string]any{"field": args.Field, "value": args.Value})
	})
}
