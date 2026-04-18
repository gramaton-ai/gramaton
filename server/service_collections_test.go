package server

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
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
	list, svcErr := srv.serviceCollectionList(&collectionListRequest{})
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

// collectionItemsFixture builds a 4-item schema'd collection for
// projection + filter tests: two open/P1 items, one open/P2, one
// closed/P3. Returns the collection id.
func collectionItemsFixture(t *testing.T, srv *Server) string {
	t.Helper()
	ctx := context.Background()
	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "closed"}},
			{Name: "severity", Type: FieldTypeEnum, Required: true, Values: []string{"P1", "P2", "P3"}},
			{Name: "details", Type: FieldTypeString, Required: false},
		},
	}
	cc, svcErr := srv.serviceCollectionCreate(ctx, &collectionCreateRequest{Name: "Bugs", Schema: schema})
	if svcErr != nil {
		t.Fatalf("create: %v", svcErr)
	}
	collID := cc["id"].(string)
	rows := []map[string]any{
		{"title": "Panic middleware", "status": "open", "severity": "P2", "details": "long-form notes a b c"},
		{"title": "Ctx propagation", "status": "open", "severity": "P1", "details": "blah blah blah"},
		{"title": "Error scrub", "status": "closed", "severity": "P3", "details": "done"},
		{"title": "Taxonomy drift", "status": "open", "severity": "P1", "details": "more notes"},
	}
	for _, row := range rows {
		if _, err := srv.serviceCollectionAdd(collID, &collectionAddRequest{Fields: row}); err != nil {
			t.Fatalf("add %q: %v", row["title"], err)
		}
	}
	return collID
}

func TestCollectionItemsFieldProjection(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Fields: []string{"title", "status"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res["items"].([]map[string]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	for _, item := range items {
		fields := item["fields"].(map[string]any)
		// Whitelist respected.
		for k := range fields {
			if k != "title" && k != "status" {
				t.Errorf("projection leaked field %q", k)
			}
		}
		if _, ok := fields["title"]; !ok {
			t.Errorf("title missing from projected fields")
		}
		if _, ok := fields["status"]; !ok {
			t.Errorf("status missing from projected fields")
		}
		// details + severity must be dropped.
		if _, ok := fields["details"]; ok {
			t.Errorf("details should be absent from projection")
		}
		if _, ok := fields["severity"]; ok {
			t.Errorf("severity should be absent from projection")
		}
		// Top-level id always present.
		if item["id"] == nil {
			t.Error("top-level id missing")
		}
	}
}

func TestCollectionItemsFilterExact(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Filter: map[string]any{"status": "closed"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 closed item, got %d", len(items))
	}
	if items[0]["fields"].(map[string]any)["status"] != "closed" {
		t.Error("filtered item does not have status=closed")
	}
}

func TestCollectionItemsFilterAnyOf(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Filter: map[string]any{"severity": []string{"P1", "P2"}},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res["items"].([]map[string]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 P1/P2 items, got %d", len(items))
	}
	for _, item := range items {
		sev := item["fields"].(map[string]any)["severity"]
		if sev != "P1" && sev != "P2" {
			t.Errorf("unexpected severity %v in filtered result", sev)
		}
	}
}

func TestCollectionItemsFilterAndProjection(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Filter: map[string]any{"status": "open", "severity": "P1"},
		Fields: []string{"title"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 open+P1 items, got %d", len(items))
	}
	for _, item := range items {
		fields := item["fields"].(map[string]any)
		if len(fields) != 1 {
			t.Errorf("expected 1 projected field, got %d (%v)", len(fields), fields)
		}
		if _, ok := fields["title"]; !ok {
			t.Error("title missing")
		}
	}
}

func TestCollectionItemsFilterUnknownFieldReturnsEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Filter: map[string]any{"status": "nonexistent"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	if n := res["count"].(int); n != 0 {
		t.Errorf("expected 0 items for unmatched filter value, got %d", n)
	}
}

func TestCollectionItemsFilterInvalidValueTypeRejected(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	_, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Filter: map[string]any{"severity": 42}, // not string, not []string
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for numeric filter value")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsHTTPProjectionAndFilter(t *testing.T) {
	srv, _ := setupTestServer(t)
	collID := collectionItemsFixture(t, srv)

	// fields=title,status  (comma-separated) + filter.status=open&filter.severity=P1,P2
	path := fmt.Sprintf("/v1/collections/%s/items?fields=title,status&filter.status=open&filter.severity=P1,P2", collID)
	w := doRequest(t, srv, "GET", path, nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	env := parseResponse(t, w)
	data := env["data"].(map[string]any)
	items := data["items"].([]any)
	// 3 open items, of which 2 are P1 and 1 is P2 -> all 3 match "open AND P1|P2".
	if len(items) != 3 {
		t.Fatalf("expected 3 matching items, got %d: %v", len(items), items)
	}
	for _, it := range items {
		fields := it.(map[string]any)["fields"].(map[string]any)
		for k := range fields {
			if k != "title" && k != "status" {
				t.Errorf("projection leaked field %q via HTTP", k)
			}
		}
		if fields["status"] != "open" {
			t.Errorf("filter failed: status=%v", fields["status"])
		}
	}
}

func TestCollectionItemsSortOnExcludedField(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, srv)

	// Sort by severity but only project title -- sort still works,
	// severity doesn't bleed into the projected output.
	res, apiErr := srv.api.CollectionItems(ctx, collID, &api.CollectionItemsRequest{
		Fields: []string{"title"},
		Sort:   "severity",
		Order:  "asc",
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res["items"].([]map[string]any)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	// severity should NOT be present in any projected fields.
	for _, item := range items {
		fields := item["fields"].(map[string]any)
		if _, present := fields["severity"]; present {
			t.Error("severity should not leak into projection after sort-on-excluded-field")
		}
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

	// Add 100 items. (Reduced from 500 to avoid timeout under parallel
	// test execution where CPU contention slows gzip compression.)
	const numItems = 100
	for i := 0; i < numItems; i++ {
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
	if items["count"].(int) != numItems {
		t.Fatalf("expected %d items, got %d", numItems, items["count"])
	}

	// List collections (with item count computation).
	list, svcErr := srv.serviceCollectionList(&collectionListRequest{})
	if svcErr != nil {
		t.Fatalf("list: %v", svcErr)
	}
	colls := list["collections"].([]map[string]any)
	if colls[0]["item_count"].(int) != numItems {
		t.Errorf("item_count = %v, want %d", colls[0]["item_count"], numItems)
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
