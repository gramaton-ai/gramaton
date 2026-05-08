package api

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCollectionCreateAndList(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:        "Sprint 23",
		Description: "Current sprint backlog",
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	id := result.ID
	if id == "" {
		t.Fatal("expected id in result")
	}

	list, apiErr := a.CollectionList(ctx, CollectionListRequest{})
	if apiErr != nil {
		t.Fatalf("list: %v", apiErr)
	}
	colls := list.Collections
	if len(colls) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(colls))
	}
	if colls[0].Name != "Sprint 23" {
		t.Errorf("name = %v, want Sprint 23", colls[0].Name)
	}
}

func TestCollectionNameUniqueness(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	_, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Backlog"})
	if apiErr != nil {
		t.Fatalf("first create: %v", apiErr)
	}

	_, apiErr = a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Backlog"})
	if apiErr == nil {
		t.Fatal("expected conflict error for duplicate name")
	}
	if apiErr.Code != "conflict" {
		t.Errorf("expected conflict code, got %s", apiErr.Code)
	}

	_, apiErr = a.CollectionCreate(ctx, CollectionCreateRequest{Name: "backlog"})
	if apiErr == nil {
		t.Fatal("expected conflict error for case-insensitive duplicate")
	}
}

func TestCollectionWithSchema(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
			{Name: "priority", Type: FieldTypeNumber, Required: false},
		},
	}

	result, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:   "Tasks",
		Schema: schema,
	})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	collID := result.ID

	addResult, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Do the thing", "status": "open"},
	})
	if apiErr != nil {
		t.Fatalf("add: %v", apiErr)
	}
	if addResult.ID == "" {
		t.Fatal("expected item id")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "No status"},
	})
	if apiErr == nil {
		t.Fatal("expected error for missing required field")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Bad status", "status": "invalid"},
	})
	if apiErr == nil {
		t.Fatal("expected error for invalid enum value")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Extra", "status": "open", "unknown": "value"},
	})
	if apiErr == nil {
		t.Fatal("expected error for unknown field")
	}
}

// TestCollectionItemsAsOfFutureRejected covers the basic validator
// path: an as_of value after "now" is input-error, same shape as
// other future-date rejections.
func TestCollectionItemsAsOfFutureRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "L"})
	collID := coll.ID

	// UTC: parseDateArg interprets YYYY-MM-DD as UTC midnight, so a
	// local-TZ-formatted "tomorrow" can decode to a past UTC time when
	// the local clock is east of UTC across the day boundary.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{AsOf: tomorrow})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for future as_of, got %+v", apiErr)
	}
}

// TestCollectionItemsAsOfInvalidDate rejects garbage as_of input.
func TestCollectionItemsAsOfInvalidDate(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "L"})
	collID := coll.ID

	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{AsOf: "nope"})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error for bad as_of, got %+v", apiErr)
	}
}

// TestCollectionItemsAsOfPointInTime exercises the happy path:
// seed a collection, add two items, capture a mid-point timestamp,
// add a third item, then verify as_of at the mid-point returns
// exactly the first two members. Point-in-time correctness.
func TestCollectionItemsAsOfPointInTime(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "PIT"})
	collID := coll.ID

	if _, err := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "first"},
	}); err != nil {
		t.Fatalf("add first: %v", err)
	}
	if _, err := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "second"},
	}); err != nil {
		t.Fatalf("add second: %v", err)
	}

	// Capture midpoint after two adds.
	time.Sleep(15 * time.Millisecond)
	midpoint := time.Now().UTC()
	time.Sleep(15 * time.Millisecond)

	if _, err := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "third"},
	}); err != nil {
		t.Fatalf("add third: %v", err)
	}

	resp, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		AsOf: midpoint.Format(time.RFC3339Nano),
	})
	if apiErr != nil {
		t.Fatalf("as_of items: %v", apiErr)
	}

	if resp.Semantics != "point_in_time" {
		t.Errorf("semantics = %q, want point_in_time", resp.Semantics)
	}
	if resp.AsOf == "" {
		t.Errorf("response missing as_of field")
	}
	count := resp.Count
	if count != 2 {
		t.Errorf("midpoint count = %d, want 2 (third was added after midpoint)", count)
	}

	// Sanity: HEAD read returns all three.
	headResp, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	if apiErr != nil {
		t.Fatalf("HEAD items: %v", apiErr)
	}
	if c := headResp.Count; c != 3 {
		t.Errorf("HEAD count = %d, want 3", c)
	}
}

// TestCollectionItemsAsOfBeforeCreation returns empty when the
// caller asks for a point before the collection's own creation
// commit. The response shape still carries as_of + semantics so
// agents can tell the difference from a HEAD-empty collection.
func TestCollectionItemsAsOfBeforeCreation(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	// Lock in a "before" timestamp first.
	before := time.Now().UTC().Add(-time.Hour)

	coll, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "LateBloomer"})
	collID := coll.ID
	_, _ = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "x"},
	})

	resp, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		AsOf: before.Format(time.RFC3339Nano),
	})
	if apiErr != nil {
		t.Fatalf("as_of: %v", apiErr)
	}
	if c := resp.Count; c != 0 {
		t.Errorf("before-creation count = %d, want 0", c)
	}
	if resp.Semantics != "point_in_time" {
		t.Errorf("semantics = %q, want point_in_time", resp.Semantics)
	}
}

func TestCollectionItemsExhaustive(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "List"})
	collID := result.ID

	for i := 0; i < 5; i++ {
		_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
			Fields: map[string]any{"title": fmt.Sprintf("Item %d", i)},
		})
		if apiErr != nil {
			t.Fatalf("add %d: %v", i, apiErr)
		}
	}

	items, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	count := items.Count
	if count != 5 {
		t.Fatalf("expected 5 items, got %d", count)
	}
}

// collectionItemsFixture builds a 4-item schema'd collection for
// projection + filter tests.
func collectionItemsFixture(t *testing.T, a *API) string {
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
	cc, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Bugs", Schema: schema})
	if apiErr != nil {
		t.Fatalf("create: %v", apiErr)
	}
	collID := cc.ID
	rows := []map[string]any{
		{"title": "Panic middleware", "status": "open", "severity": "P2", "details": "long-form notes a b c"},
		{"title": "Ctx propagation", "status": "open", "severity": "P1", "details": "blah blah blah"},
		{"title": "Error scrub", "status": "closed", "severity": "P3", "details": "done"},
		{"title": "Taxonomy drift", "status": "open", "severity": "P1", "details": "more notes"},
	}
	for _, row := range rows {
		if _, err := a.CollectionAdd(ctx, collID, CollectionAddRequest{Fields: row}); err != nil {
			t.Fatalf("add %q: %v", row["title"], err)
		}
	}
	return collID
}

func TestCollectionItemsFieldProjection(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Fields: []string{"title", "status"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res.Items
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	for _, item := range items {
		fields := item.Fields
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
		if _, ok := fields["details"]; ok {
			t.Errorf("details should be absent from projection")
		}
		if _, ok := fields["severity"]; ok {
			t.Errorf("severity should be absent from projection")
		}
		if item.ID == "" {
			t.Error("top-level id missing")
		}
	}
}

func TestCollectionItemsFilterExact(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"status": "closed"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res.Items
	if len(items) != 1 {
		t.Fatalf("expected 1 closed item, got %d", len(items))
	}
	if items[0].Fields["status"] != "closed" {
		t.Error("filtered item does not have status=closed")
	}
}

func TestCollectionItemsFilterAnyOf(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"severity": []string{"P1", "P2"}},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res.Items
	if len(items) != 3 {
		t.Fatalf("expected 3 P1/P2 items, got %d", len(items))
	}
	for _, item := range items {
		sev := item.Fields["severity"]
		if sev != "P1" && sev != "P2" {
			t.Errorf("unexpected severity %v in filtered result", sev)
		}
	}
}

func TestCollectionItemsFilterAndProjection(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"status": "open", "severity": "P1"},
		Fields: []string{"title"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res.Items
	if len(items) != 2 {
		t.Fatalf("expected 2 open+P1 items, got %d", len(items))
	}
	for _, item := range items {
		fields := item.Fields
		if len(fields) != 1 {
			t.Errorf("expected 1 projected field, got %d (%v)", len(fields), fields)
		}
		if _, ok := fields["title"]; !ok {
			t.Error("title missing")
		}
	}
}

func TestCollectionItemsFilterUnknownFieldReturnsEmpty(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"status": "nonexistent"},
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	if n := res.Count; n != 0 {
		t.Errorf("expected 0 items for unmatched filter value, got %d", n)
	}
}

func TestCollectionItemsFilterInvalidValueTypeRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"severity": 42},
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for numeric filter value")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsProjectionCapExceeded(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	tooMany := make([]string, MaxProjectionFields+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("f%d", i)
	}
	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{Fields: tooMany})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for projection cap")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsFilterKeyCapExceeded(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	filter := make(map[string]any, MaxFilterKeys+1)
	for i := 0; i <= MaxFilterKeys; i++ {
		filter[fmt.Sprintf("f%d", i)] = "x"
	}
	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{Filter: filter})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for filter key cap")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsFilterValueCapExceeded(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	tooMany := make([]string, MaxFilterValuesPerKey+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("v%d", i)
	}
	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Filter: map[string]any{"status": tooMany},
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for filter value cap")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsProjectionInvalidFieldNameRejected(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	_, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Fields: []string{"title", "not a field name"},
	})
	if apiErr == nil {
		t.Fatal("expected ErrInvalid for malformed projection field name")
	}
	if apiErr.Code != "input_error" {
		t.Errorf("expected input_error, got %s", apiErr.Code)
	}
}

func TestCollectionItemsSortOnExcludedField(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()
	collID := collectionItemsFixture(t, a)

	res, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{
		Fields: []string{"title"},
		Sort:   "severity",
		Order:  "asc",
	})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	items := res.Items
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	for _, item := range items {
		fields := item.Fields
		if _, present := fields["severity"]; present {
			t.Error("severity should not leak into projection after sort-on-excluded-field")
		}
	}
}

func TestCollectionRemovePreservesNode(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "List"})
	collID := result.ID

	addResult, _ := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Keep me"},
	})
	itemID := addResult.ID

	_, apiErr := a.CollectionRemove(ctx, collID, itemID)
	if apiErr != nil {
		t.Fatalf("remove: %v", apiErr)
	}

	items, _ := a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	if items.Count != 0 {
		t.Fatal("expected 0 items after remove")
	}

	eng.RLock()
	_, ok := eng.Graph().GetNode(itemID)
	eng.RUnlock()
	if !ok {
		t.Fatal("node should still exist after remove")
	}
}

func TestCollectionUpdate(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
		},
	}

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Tasks", Schema: schema})
	collID := result.ID

	addResult, _ := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Task 1", "status": "open"},
	})
	itemID := addResult.ID

	_, apiErr := a.CollectionUpdate(ctx, collID, itemID, CollectionUpdateRequest{
		Fields: map[string]any{"status": "done"},
	})
	if apiErr != nil {
		t.Fatalf("update: %v", apiErr)
	}

	items, _ := a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	itemList := items.Items
	fields := itemList[0].Fields
	if fields["status"] != "done" {
		t.Errorf("status = %v, want done", fields["status"])
	}

	_, apiErr = a.CollectionUpdate(ctx, collID, itemID, CollectionUpdateRequest{
		Fields: map[string]any{"status": "invalid"},
	})
	if apiErr == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestCollectionMove(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Backlog"})
	r2, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Active"})
	backlogID := r1.ID
	activeID := r2.ID

	addResult, _ := a.CollectionAdd(ctx, backlogID, CollectionAddRequest{
		Fields: map[string]any{"title": "Do it"},
	})
	itemID := addResult.ID

	_, apiErr := a.CollectionMove(ctx, backlogID, itemID, CollectionMoveRequest{
		TargetCollectionID: activeID,
	})
	if apiErr != nil {
		t.Fatalf("move: %v", apiErr)
	}

	backlogItems, _ := a.CollectionItems(ctx, backlogID, CollectionItemsRequest{})
	if backlogItems.Count != 0 {
		t.Error("backlog should be empty after move")
	}
	activeItems, _ := a.CollectionItems(ctx, activeID, CollectionItemsRequest{})
	if activeItems.Count != 1 {
		t.Error("active should have 1 item after move")
	}
}

func TestCollectionRetireUnretire(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Temp"})
	collID := result.ID

	a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Item"},
	})

	retireResult, apiErr := a.CollectionDelete(ctx, collID)
	if apiErr != nil {
		t.Fatalf("retire: %v", apiErr)
	}
	if retireResult.Retired != true {
		t.Fatal("expected retired=true")
	}
	if retireResult.ItemsPreserved != 1 {
		t.Error("expected 1 item preserved")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Nope"},
	})
	if apiErr == nil {
		t.Fatal("expected error adding to retired collection")
	}

	unretireResult, apiErr := a.CollectionDelete(ctx, collID)
	if apiErr != nil {
		t.Fatalf("unretire: %v", apiErr)
	}
	if unretireResult.Unretired != true {
		t.Fatal("expected unretired=true")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Back in business"},
	})
	if apiErr != nil {
		t.Fatalf("add after unretire: %v", apiErr)
	}
}

func TestCollectionMultiMembership(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Sprint 23"})
	r2, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Security"})
	sprintID := r1.ID
	securityID := r2.ID

	addResult, _ := a.CollectionAdd(ctx, sprintID, CollectionAddRequest{
		Fields: map[string]any{"title": "Fix auth bug"},
	})
	itemID := addResult.ID

	// Create the second membership edge manually: api doesn't expose an
	// "add to additional collection" operation, but member_of edges are
	// the underlying representation.
	eng.Lock()
	if _, err := eng.Graph().AddEdge(itemID, securityID, "member_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("add membership edge: %v", err)
	}
	if _, err := eng.Save("test_multi_membership"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	sprintItems, _ := a.CollectionItems(ctx, sprintID, CollectionItemsRequest{})
	securityItems, _ := a.CollectionItems(ctx, securityID, CollectionItemsRequest{})
	if sprintItems.Count != 1 {
		t.Error("sprint should have 1 item")
	}
	if securityItems.Count != 1 {
		t.Error("security should have 1 item")
	}

	a.CollectionRemove(ctx, sprintID, itemID)
	sprintItems, _ = a.CollectionItems(ctx, sprintID, CollectionItemsRequest{})
	securityItems, _ = a.CollectionItems(ctx, securityID, CollectionItemsRequest{})
	if sprintItems.Count != 0 {
		t.Error("sprint should be empty")
	}
	if securityItems.Count != 1 {
		t.Error("security should still have 1 item")
	}
}

func TestCollectionSchemaEvolution(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
		},
	}
	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Tasks", Schema: schema})
	collID := result.ID

	a.CollectionAdd(ctx, collID, CollectionAddRequest{Fields: map[string]any{"title": "Task A"}})
	a.CollectionAdd(ctx, collID, CollectionAddRequest{Fields: map[string]any{"title": "Task B"}})

	newSchema := CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "priority", Type: FieldTypeEnum, Required: true, Values: []string{"p0", "p1", "p2"}},
		},
	}
	updateResult, apiErr := a.CollectionSchemaUpdate(ctx, collID, CollectionSchemaUpdateRequest{
		Schema: newSchema,
	})
	if apiErr != nil {
		t.Fatalf("schema update: %v", apiErr)
	}
	migration := updateResult.Migration
	if migration.Total != 2 {
		t.Errorf("expected 2 items needing migration, got %d", migration.Total)
	}

	items, _ := a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	itemList := items.Items
	for _, item := range itemList {
		if item.NeedsMigration == nil {
			t.Error("expected needs_migration annotation on pre-migration item")
		}
	}

	migrateResult, apiErr := a.CollectionMigrate(ctx, collID, CollectionMigrateRequest{
		Field: "priority",
		Value: "p2",
	})
	if apiErr != nil {
		t.Fatalf("migrate: %v", apiErr)
	}
	if migrateResult.Migrated != 2 {
		t.Errorf("expected 2 migrated, got %v", migrateResult.Migrated)
	}
	if migrateResult.MigrationComplete != true {
		t.Error("expected migration_complete=true")
	}

	items, _ = a.CollectionItems(ctx, collID, CollectionItemsRequest{})
	if items.Migration != nil {
		t.Error("expected no migration state after completion")
	}
}

// TestCollectionAddIdempotentOnCurationNone covers the idempotent-add
// path: collections with curation=none (shopping-list / packing-list
// shape) make duplicate adds idempotent instead of returning
// ErrConflict. Short-content items like "eggs" or "milk" treat
// identical content as the same item; the response surfaces
// deduplicated=true + the existing ID.
func TestCollectionAddIdempotentOnCurationNone(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "Groceries",
		Curation: "none",
	})
	collID := result.ID

	first, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "eggs"},
	})
	if apiErr != nil {
		t.Fatalf("first add: %v", apiErr)
	}
	firstID := first.ID

	// Second add: same content -> idempotent success with existing ID.
	second, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "eggs"},
	})
	if apiErr != nil {
		t.Fatalf("second add should be idempotent, got: %v", apiErr)
	}
	if second.ID != firstID {
		t.Errorf("second add id = %v, want %q (existing)", second.ID, firstID)
	}
	if !second.Deduplicated {
		t.Errorf("second add should flag deduplicated=true, got %+v", second)
	}

	// Trim + case-insensitive: " EGGS " matches "eggs".
	third, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": " EGGS "},
	})
	if apiErr != nil {
		t.Fatalf("third add (case/trim variant): %v", apiErr)
	}
	if third.ID != firstID {
		t.Errorf("case/trim variant id = %v, want %q", third.ID, firstID)
	}
}

// TestCollectionAddProcessingStatusGatedByCuration pins the
// commit-4 contract: collection_add stamps the new item's
// processing_status based on the resolved curation knob. Items in
// curation=standard collections are eligible for the autonomous
// pipeline (captured); items in curation=none collections bypass
// it (processed). This is what makes the curation knob mean
// something at the per-record level.
func TestCollectionAddProcessingStatusGatedByCuration(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	// curation=standard requires content_fields declared on the
	// schema (enforced at create time).
	std, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "StandardColl",
		Curation: "standard",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
			},
			ContentFields: []string{"title"},
		},
	})
	stdItem, apiErr := a.CollectionAdd(ctx, std.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "thing"},
	})
	if apiErr != nil {
		t.Fatalf("add to standard collection: %v", apiErr)
	}
	stdNode, _ := eng.Graph().GetNode(stdItem.ID)
	if got, _ := stdNode.Properties.GetString("processing_status"); got != "captured" {
		t.Errorf("standard collection item: processing_status = %q, want captured", got)
	}

	// curation=none.
	none, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "NoneColl",
		Curation: "none",
	})
	noneItem, apiErr := a.CollectionAdd(ctx, none.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "thing"},
	})
	if apiErr != nil {
		t.Fatalf("add to none collection: %v", apiErr)
	}
	noneNode, _ := eng.Graph().GetNode(noneItem.ID)
	if got, _ := noneNode.Properties.GetString("processing_status"); got != "processed" {
		t.Errorf("none collection item: processing_status = %q, want processed", got)
	}
}

// TestCollectionAddBatchProcessingStatusGatedByCuration mirrors the
// single-add gate for the batch path. The status decision is hoisted
// once per call and applied to every item in the batch.
func TestCollectionAddBatchProcessingStatusGatedByCuration(t *testing.T) {
	a, eng := setupTestAPI(t)
	ctx := context.Background()

	std, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "StandardBatch",
		Curation: "standard",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
			},
			ContentFields: []string{"title"},
		},
	})
	stdResp, apiErr := a.CollectionAddBatch(ctx, std.ID, CollectionAddBatchRequest{
		Items: []CollectionAddItem{
			{Fields: map[string]any{"title": "first"}},
			{Fields: map[string]any{"title": "second"}},
		},
	})
	if apiErr != nil {
		t.Fatalf("batch add to standard collection: %v", apiErr)
	}
	if len(stdResp.Added) != 2 {
		t.Fatalf("standard batch: Added = %d, want 2", len(stdResp.Added))
	}
	for _, a := range stdResp.Added {
		n, _ := eng.Graph().GetNode(a.ID)
		if got, _ := n.Properties.GetString("processing_status"); got != "captured" {
			t.Errorf("standard batch item %s: processing_status = %q, want captured", a.ID, got)
		}
	}

	none, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "NoneBatch",
		Curation: "none",
	})
	noneResp, apiErr := a.CollectionAddBatch(ctx, none.ID, CollectionAddBatchRequest{
		Items: []CollectionAddItem{
			{Fields: map[string]any{"title": "first"}},
			{Fields: map[string]any{"title": "second"}},
		},
	})
	if apiErr != nil {
		t.Fatalf("batch add to none collection: %v", apiErr)
	}
	if len(noneResp.Added) != 2 {
		t.Fatalf("none batch: Added = %d, want 2", len(noneResp.Added))
	}
	for _, a := range noneResp.Added {
		n, _ := eng.Graph().GetNode(a.ID)
		if got, _ := n.Properties.GetString("processing_status"); got != "processed" {
			t.Errorf("none batch item %s: processing_status = %q, want processed", a.ID, got)
		}
	}
}

// TestCollectionDedup pins the canonical-api behavior: CollectionAdd
// rejects a duplicate-title add with ErrConflict (instead of the
// older success-with-duplicate=true map). The existing item's ID
// is surfaced in the error message.
func TestCollectionDedup(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	// curation=standard collections reject duplicate titles with
	// ErrConflict (this test's contract); curation=none collections
	// dedup idempotently. Use standard explicitly.
	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:     "List",
		Curation: "standard",
		Schema: &CollectionSchema{
			Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString, Required: true},
			},
			ContentFields: []string{"title"},
		},
	})
	collID := result.ID

	a.CollectionAdd(ctx, collID, CollectionAddRequest{Fields: map[string]any{"title": "Buy milk"}})

	_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Buy milk"},
	})
	if apiErr == nil {
		t.Fatal("expected conflict error on duplicate add")
	}
	if apiErr.Code != "conflict" {
		t.Errorf("expected conflict code, got %s", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "already exists") {
		t.Errorf("expected 'already exists' in error message, got %q", apiErr.Message)
	}
}

func TestCollectionRename(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	r1, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Old Name"})
	a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Taken"})
	collID := r1.ID

	_, apiErr := a.CollectionRename(ctx, collID, CollectionRenameRequest{Name: "New Name"})
	if apiErr != nil {
		t.Fatalf("rename: %v", apiErr)
	}

	_, apiErr = a.CollectionRename(ctx, collID, CollectionRenameRequest{Name: "Taken"})
	if apiErr == nil {
		t.Fatal("expected conflict error")
	}

	_, apiErr = a.CollectionRename(ctx, collID, CollectionRenameRequest{Name: "New Name"})
	if apiErr != nil {
		t.Fatalf("rename to self: %v", apiErr)
	}
}

func TestCollectionFieldNameValidation(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "List"})
	collID := result.ID

	_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title.evil": "injected"},
	})
	if apiErr == nil {
		t.Fatal("expected error for field name with dots")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"../escape": "bad"},
	})
	if apiErr == nil {
		t.Fatal("expected error for field name with slashes")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"valid_field_name": "good"},
	})
	if apiErr != nil {
		t.Fatalf("valid field name rejected: %v", apiErr)
	}
}

func TestCollectionSchemaFieldNameValidation(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "field.with.dots", Type: FieldTypeString, Required: true},
		},
	}
	_, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:   "Bad Schema",
		Schema: schema,
	})
	if apiErr == nil {
		t.Fatal("expected error for schema field name with dots")
	}
}

func TestCollectionNaNRejection(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "score", Type: FieldTypeNumber, Required: true},
		},
	}
	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Scores", Schema: schema})
	collID := result.ID

	_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"score": math.NaN()},
	})
	if apiErr == nil {
		t.Fatal("expected error for NaN value")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"score": math.Inf(1)},
	})
	if apiErr == nil {
		t.Fatal("expected error for Inf value")
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"score": 42.5},
	})
	if apiErr != nil {
		t.Fatalf("valid number rejected: %v", apiErr)
	}
}

func TestCollectionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test")
	}
	if runtime.GOOS == "windows" {
		// Perf-shape test; structural correctness is covered by smaller
		// sibling tests (TestCollectionAdd*, TestCollectionItems*). On
		// slow Windows CI runners this consumes ~15-50s of the api
		// package's 10-min budget under race + parallel-suite load,
		// pushing the package over its budget. Linux/macOS still run
		// full size.
		t.Skip("perf-shape test; structural correctness covered by smaller siblings; saves Windows CI budget")
	}
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "status", Type: FieldTypeEnum, Required: true, Values: []string{"open", "done"}},
			{Name: "priority", Type: FieldTypeNumber, Required: false},
		},
	}
	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Perf Test", Schema: schema})
	collID := result.ID

	const numItems = 100
	for i := 0; i < numItems; i++ {
		_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
			Fields: map[string]any{
				"title":    fmt.Sprintf("Task %d", i),
				"status":   "open",
				"priority": float64(i % 4),
			},
		})
		if apiErr != nil {
			t.Fatalf("add %d: %v", i, apiErr)
		}
	}

	items, apiErr := a.CollectionItems(ctx, collID, CollectionItemsRequest{Sort: "priority"})
	if apiErr != nil {
		t.Fatalf("items: %v", apiErr)
	}
	if items.Count != numItems {
		t.Fatalf("expected %d items, got %d", numItems, items.Count)
	}

	list, apiErr := a.CollectionList(ctx, CollectionListRequest{})
	if apiErr != nil {
		t.Fatalf("list: %v", apiErr)
	}
	colls := list.Collections
	if colls[0].ItemCount != numItems {
		t.Errorf("item_count = %v, want %d", colls[0].ItemCount, numItems)
	}
}

func TestCollectionEnumSet(t *testing.T) {
	a, _ := setupTestAPI(t)
	ctx := context.Background()

	schema := &CollectionSchema{
		Fields: []SchemaField{
			{Name: "title", Type: FieldTypeString, Required: true},
			{Name: "labels", Type: FieldTypeEnumSet, Required: false, Values: []string{"bug", "feature", "security"}},
		},
	}

	result, _ := a.CollectionCreate(ctx, CollectionCreateRequest{Name: "Issues", Schema: schema})
	collID := result.ID

	_, apiErr := a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Fix crash", "labels": []any{"bug", "security"}},
	})
	if apiErr != nil {
		t.Fatalf("add with enum[]: %v", apiErr)
	}

	_, apiErr = a.CollectionAdd(ctx, collID, CollectionAddRequest{
		Fields: map[string]any{"title": "Bad label", "labels": []any{"bug", "invalid"}},
	})
	if apiErr == nil {
		t.Fatal("expected error for invalid enum[] value")
	}
}
