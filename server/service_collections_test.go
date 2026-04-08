package server

import (
	"context"
	"fmt"
	"math"
	"testing"
)

func TestCollectionCreateAndList(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	// Create a collection.
	result, svcErr := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{
		Name:        "Sprint 23",
		Description: "Current sprint backlog",
	})
	if svcErr != nil {
		t.Fatalf("create: %v", svcErr)
	}
	id, ok := result["id"].(string)
	if !ok || id == "" {
		t.Fatal("expected id in result")
	}

	// List should return it.
	list, svcErr := srv.serviceCollectionList()
	if svcErr != nil {
		t.Fatalf("list: %v", svcErr)
	}
	colls := list["collections"].([]map[string]any)
	if len(colls) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(colls))
	}
	if colls[0]["name"] != "Sprint 23" {
		t.Errorf("name = %v, want Sprint 23", colls[0]["name"])
	}
}

func TestCollectionNameUniqueness(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	_, svcErr := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Backlog"})
	if svcErr != nil {
		t.Fatalf("first create: %v", svcErr)
	}

	// Same name should fail.
	_, svcErr = srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Backlog"})
	if svcErr == nil {
		t.Fatal("expected conflict error for duplicate name")
	}
	if svcErr.Code != "duplicate" {
		t.Errorf("expected duplicate code, got %s", svcErr.Code)
	}

	// Case-insensitive.
	_, svcErr = srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "backlog"})
	if svcErr == nil {
		t.Fatal("expected conflict error for case-insensitive duplicate")
	}
}

func TestCollectionWithSchema(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
			{Name: "priority", Type: FieldTypeNumber, Required: false},
		},
	}

	result, svcErr := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{
		Name:   "Tasks",
		Schema: schema,
	})
	if svcErr != nil {
		t.Fatalf("create: %v", svcErr)
	}
	collID := result["id"].(string)

	// Add valid item.
	addResult, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Do the thing", "status": "open"},
	})
	if svcErr != nil {
		t.Fatalf("add: %v", svcErr)
	}
	if addResult["id"] == nil {
		t.Fatal("expected item id")
	}

	// Missing required field should fail.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "No status"},
	})
	if svcErr == nil {
		t.Fatal("expected error for missing required field")
	}

	// Invalid enum value should fail.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Bad status", "status": "invalid"},
	})
	if svcErr == nil {
		t.Fatal("expected error for invalid enum value")
	}

	// Unknown field should fail.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Extra", "status": "open", "unknown": "value"},
	})
	if svcErr == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestCollectionItemsExhaustive(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "List"})
	collID := result["id"].(string)

	// Add 5 items.
	for i := 0; i < 5; i++ {
		_, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
			Fields: map[string]any{"title": fmt.Sprintf("Item %d", i)},
		})
		if svcErr != nil {
			t.Fatalf("add %d: %v", i, svcErr)
		}
	}

	// List should return all 5.
	items, svcErr := srv.serviceCollectionItems(collID, &collectionItemsRequest{})
	if svcErr != nil {
		t.Fatalf("items: %v", svcErr)
	}
	count := items["count"].(int)
	if count != 5 {
		t.Fatalf("expected 5 items, got %d", count)
	}
}

func TestCollectionRemovePreservesNode(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "List"})
	collID := result["id"].(string)

	addResult, _ := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Keep me"},
	})
	itemID := addResult["id"].(string)

	// Remove from collection.
	_, svcErr := srv.serviceCollectionRemove(collID, itemID)
	if svcErr != nil {
		t.Fatalf("remove: %v", svcErr)
	}

	// Item count should be 0.
	items, _ := srv.serviceCollectionItems(collID, &collectionItemsRequest{})
	if items["count"].(int) != 0 {
		t.Fatal("expected 0 items after remove")
	}

	// But the node should still exist in the graph.
	srv.engine.RLock()
	_, ok := srv.engine.Graph().GetNode(itemID)
	srv.engine.RUnlock()
	if !ok {
		t.Fatal("node should still exist after remove")
	}
}

func TestCollectionUpdate(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
		},
	}

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Tasks", Schema: schema})
	collID := result["id"].(string)

	addResult, _ := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Task 1", "status": "open"},
	})
	itemID := addResult["id"].(string)

	// Update status.
	_, svcErr := srv.serviceCollectionUpdate(collID, itemID, &collectionUpdateRequest{
		Fields: map[string]any{"status": "done"},
	})
	if svcErr != nil {
		t.Fatalf("update: %v", svcErr)
	}

	// Verify.
	items, _ := srv.serviceCollectionItems(collID, &collectionItemsRequest{})
	itemList := items["items"].([]map[string]any)
	fields := itemList[0]["fields"].(map[string]any)
	if fields["status"] != "done" {
		t.Errorf("status = %v, want done", fields["status"])
	}

	// Invalid update should fail.
	_, svcErr = srv.serviceCollectionUpdate(collID, itemID, &collectionUpdateRequest{
		Fields: map[string]any{"status": "invalid"},
	})
	if svcErr == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestCollectionMove(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	r1, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Backlog"})
	r2, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Active"})
	backlogID := r1["id"].(string)
	activeID := r2["id"].(string)

	addResult, _ := srv.serviceCollectionAdd(backlogID, &collectionAddRequest{
		Fields: map[string]any{"title": "Do it"},
	})
	itemID := addResult["id"].(string)

	// Move to Active.
	_, svcErr := srv.serviceCollectionMove(backlogID, itemID, &collectionMoveRequest{
		TargetCollectionID: activeID,
	})
	if svcErr != nil {
		t.Fatalf("move: %v", svcErr)
	}

	// Backlog should be empty, Active should have 1.
	backlogItems, _ := srv.serviceCollectionItems(backlogID, &collectionItemsRequest{})
	if backlogItems["count"].(int) != 0 {
		t.Error("backlog should be empty after move")
	}
	activeItems, _ := srv.serviceCollectionItems(activeID, &collectionItemsRequest{})
	if activeItems["count"].(int) != 1 {
		t.Error("active should have 1 item after move")
	}
}

func TestCollectionRetireUnretire(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Temp"})
	collID := result["id"].(string)

	// Add an item.
	srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Item"},
	})

	// Retire.
	retireResult, svcErr := srv.serviceCollectionDelete(collID)
	if svcErr != nil {
		t.Fatalf("retire: %v", svcErr)
	}
	if retireResult["retired"] != true {
		t.Fatal("expected retired=true")
	}
	if retireResult["items_preserved"].(int) != 1 {
		t.Error("expected 1 item preserved")
	}

	// Can't add to retired collection.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Nope"},
	})
	if svcErr == nil {
		t.Fatal("expected error adding to retired collection")
	}

	// Unretire (call delete again).
	unretireResult, svcErr := srv.serviceCollectionDelete(collID)
	if svcErr != nil {
		t.Fatalf("unretire: %v", svcErr)
	}
	if unretireResult["unretired"] != true {
		t.Fatal("expected unretired=true")
	}

	// Should be able to add again.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Back in business"},
	})
	if svcErr != nil {
		t.Fatalf("add after unretire: %v", svcErr)
	}
}

func TestCollectionMultiMembership(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	r1, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Sprint 23"})
	r2, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Security"})
	sprintID := r1["id"].(string)
	securityID := r2["id"].(string)

	// Add item to Sprint.
	addResult, _ := srv.serviceCollectionAdd(sprintID, &collectionAddRequest{
		Fields: map[string]any{"title": "Fix auth bug"},
	})
	itemID := addResult["id"].(string)

	// Also add to Security (create edge manually since item already exists).
	srv.engine.Lock()
	srv.engine.Graph().AddEdge(itemID, securityID, "member_of", 1.0, nil)
	srv.engine.Save("test_multi_membership")
	srv.engine.Unlock()

	// Both should show the item.
	sprintItems, _ := srv.serviceCollectionItems(sprintID, &collectionItemsRequest{})
	securityItems, _ := srv.serviceCollectionItems(securityID, &collectionItemsRequest{})
	if sprintItems["count"].(int) != 1 {
		t.Error("sprint should have 1 item")
	}
	if securityItems["count"].(int) != 1 {
		t.Error("security should have 1 item")
	}

	// Remove from sprint -- should still be in security.
	srv.serviceCollectionRemove(sprintID, itemID)
	sprintItems, _ = srv.serviceCollectionItems(sprintID, &collectionItemsRequest{})
	securityItems, _ = srv.serviceCollectionItems(securityID, &collectionItemsRequest{})
	if sprintItems["count"].(int) != 0 {
		t.Error("sprint should be empty")
	}
	if securityItems["count"].(int) != 1 {
		t.Error("security should still have 1 item")
	}
}

func TestCollectionSchemaEvolution(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	// Create with simple schema.
	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
	}
	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Tasks", Schema: schema})
	collID := result["id"].(string)

	// Add items.
	srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Task A"},
	})
	srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Task B"},
	})

	// Update schema to add required field.
	newSchema := CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "priority", Type: FieldTypeEnum, Required: true, Values: []string{"p0", "p1", "p2"}},
		},
	}
	updateResult, svcErr := srv.serviceCollectionSchemaUpdate(collID, &collectionSchemaUpdateRequest{
		Schema: newSchema,
	})
	if svcErr != nil {
		t.Fatalf("schema update: %v", svcErr)
	}
	migration := updateResult["migration"].(map[string]any)
	total := migration["total"]
	switch v := total.(type) {
	case int:
		if v != 2 {
			t.Errorf("expected 2 items needing migration, got %v", v)
		}
	case int64:
		if v != 2 {
			t.Errorf("expected 2 items needing migration, got %v", v)
		}
	default:
		t.Fatalf("unexpected type for total: %T", total)
	}

	// Items should show needs_migration.
	items, _ := srv.serviceCollectionItems(collID, &collectionItemsRequest{})
	itemList := items["items"].([]map[string]any)
	for _, item := range itemList {
		if item["needs_migration"] == nil {
			t.Error("expected needs_migration annotation on pre-migration item")
		}
	}

	// Migrate all items.
	migrateResult, svcErr := srv.serviceCollectionMigrate(collID, &collectionMigrateRequest{
		Field: "priority",
		Value: "p2",
	})
	if svcErr != nil {
		t.Fatalf("migrate: %v", svcErr)
	}
	if migrateResult["migrated"].(int) != 2 {
		t.Errorf("expected 2 migrated, got %v", migrateResult["migrated"])
	}
	if migrateResult["migration_complete"].(bool) != true {
		t.Error("expected migration_complete=true")
	}

	// Items should no longer need migration.
	items, _ = srv.serviceCollectionItems(collID, &collectionItemsRequest{})
	if items["migration"] != nil {
		t.Error("expected no migration state after completion")
	}
}

func TestCollectionDedup(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "List"})
	collID := result["id"].(string)

	srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Buy milk"},
	})

	// Same title should return duplicate info.
	dupResult, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Buy milk"},
	})
	if svcErr != nil {
		t.Fatalf("dedup should not error: %v", svcErr)
	}
	if dupResult["duplicate"] != true {
		t.Fatal("expected duplicate=true")
	}
	if dupResult["existing_id"] == nil {
		t.Fatal("expected existing_id in duplicate response")
	}
}

func TestCollectionRename(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	r1, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Old Name"})
	srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Taken"})
	collID := r1["id"].(string)

	// Rename to new name.
	_, svcErr := srv.serviceCollectionRename(collID, &collectionRenameRequest{Name: "New Name"})
	if svcErr != nil {
		t.Fatalf("rename: %v", svcErr)
	}

	// Rename to taken name should fail.
	_, svcErr = srv.serviceCollectionRename(collID, &collectionRenameRequest{Name: "Taken"})
	if svcErr == nil {
		t.Fatal("expected conflict error")
	}

	// Rename to same name (self) should succeed.
	_, svcErr = srv.serviceCollectionRename(collID, &collectionRenameRequest{Name: "New Name"})
	if svcErr != nil {
		t.Fatalf("rename to self: %v", svcErr)
	}
}

func TestCollectionFieldNameValidation(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "List"})
	collID := result["id"].(string)

	// Field name with dots should be rejected (property key injection).
	_, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title.evil": "injected"},
	})
	if svcErr == nil {
		t.Fatal("expected error for field name with dots")
	}

	// Field name with slashes should be rejected.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"../escape": "bad"},
	})
	if svcErr == nil {
		t.Fatal("expected error for field name with slashes")
	}

	// Valid field name should work.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"valid_field_name": "good"},
	})
	if svcErr != nil {
		t.Fatalf("valid field name rejected: %v", svcErr)
	}
}

func TestCollectionSchemaFieldNameValidation(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	// Schema with invalid field name should be rejected.
	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "field.with.dots", Type: FieldTypeString, Required: true},
		},
	}
	_, svcErr := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{
		Name:   "Bad Schema",
		Schema: schema,
	})
	if svcErr == nil {
		t.Fatal("expected error for schema field name with dots")
	}
}

func TestCollectionNaNRejection(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "score", Type: FieldTypeNumber, Required: true},
		},
	}
	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Scores", Schema: schema})
	collID := result["id"].(string)

	// NaN should be rejected.
	_, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"score": math.NaN()},
	})
	if svcErr == nil {
		t.Fatal("expected error for NaN value")
	}

	// Inf should be rejected.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"score": math.Inf(1)},
	})
	if svcErr == nil {
		t.Fatal("expected error for Inf value")
	}

	// Normal number should work.
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"score": 42.5},
	})
	if svcErr != nil {
		t.Fatalf("valid number rejected: %v", svcErr)
	}
}

func TestCollectionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test")
	}
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
			{Name: "priority", Type: FieldTypeNumber, Required: false},
		},
	}
	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Perf Test", Schema: schema})
	collID := result["id"].(string)

	// Add 500 items.
	for i := 0; i < 500; i++ {
		_, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
			Fields: map[string]any{
				"title":    fmt.Sprintf("Task %d", i),
				"status":   "open",
				"priority": float64(i % 4),
			},
		})
		if svcErr != nil {
			t.Fatalf("add %d: %v", i, svcErr)
		}
	}

	// List all items.
	items, svcErr := srv.serviceCollectionItems(collID, &collectionItemsRequest{Sort: "priority"})
	if svcErr != nil {
		t.Fatalf("items: %v", svcErr)
	}
	if items["count"].(int) != 500 {
		t.Fatalf("expected 500 items, got %d", items["count"])
	}

	// List collections (with item count computation).
	list, svcErr := srv.serviceCollectionList()
	if svcErr != nil {
		t.Fatalf("list: %v", svcErr)
	}
	colls := list["collections"].([]map[string]any)
	if colls[0]["item_count"].(int) != 500 {
		t.Errorf("item_count = %v, want 500", colls[0]["item_count"])
	}
}

func TestCollectionEnumSet(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "labels", Type: FieldTypeEnumSet, Required: false, Values: []string{"bug", "feature", "security"}},
		},
	}

	result, _ := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Issues", Schema: schema})
	collID := result["id"].(string)

	// Valid enum[].
	_, svcErr := srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Fix crash", "labels": []any{"bug", "security"}},
	})
	if svcErr != nil {
		t.Fatalf("add with enum[]: %v", svcErr)
	}

	// Invalid value in enum[].
	_, svcErr = srv.serviceCollectionAdd(collID, &collectionAddRequest{
		Fields: map[string]any{"title": "Bad label", "labels": []any{"bug", "invalid"}},
	})
	if svcErr == nil {
		t.Fatal("expected error for invalid enum[] value")
	}
}
