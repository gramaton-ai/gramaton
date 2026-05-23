package api

import (
	"context"
	"testing"
)

// helper: create a collection from a template, return its ID.
func newCollFromTemplate(t *testing.T, a *API, name, template string) string {
	t.Helper()
	got, err := a.CollectionCreate(context.Background(), CollectionCreateRequest{
		Name:     name,
		Template: template,
	})
	if err != nil {
		t.Fatalf("CollectionCreate(%s, %s): %v", name, template, err)
	}
	id := got.ID
	if id == "" {
		t.Fatalf("CollectionCreate(%s, %s): no id in response %+v", name, template, got)
	}
	return id
}

// helper: add an item to a collection with the given fields, return its ID.
func newCollItem(t *testing.T, a *API, collID string, fields map[string]any) string {
	t.Helper()
	got, err := a.CollectionAdd(context.Background(), collID, CollectionAddRequest{Fields: fields})
	if err != nil {
		t.Fatalf("CollectionAdd: %v", err)
	}
	id := got.ID
	if id == "" {
		t.Fatalf("CollectionAdd: no id in response %+v", got)
	}
	return id
}

// helper: read an item's field value (string).
func readItemField(t *testing.T, a *API, itemID, fieldName string) (string, bool) {
	t.Helper()
	a.engine.RLock()
	defer a.engine.RUnlock()
	n, ok := a.engine.Graph().GetNode(itemID)
	if !ok {
		t.Fatalf("item %s not found", itemID)
	}
	v, ok := n.Properties.GetString("field." + fieldName)
	return v, ok
}

// TestResolveAutoCloseBacklogCompleted: completed → "resolved" in
// backlog's enum [open, in_progress, resolved, abandoned].
func TestResolveAutoCloseBacklogCompleted(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "dev", "backlog")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "fix bug",
		"status": "open",
	})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got, ok := resp.AutoClosedStatus["dev"]; !ok || got != "resolved" {
		t.Errorf("AutoClosedStatus[dev] = (%q, %v), want (resolved, true)", got, ok)
	}
	if resp.CollectionWarning != "" {
		t.Errorf("expected no warning when auto-close succeeds; got %q", resp.CollectionWarning)
	}
	if status, _ := readItemField(t, a, itemID, "status"); status != "resolved" {
		t.Errorf("item.status = %q after resolve, want resolved", status)
	}
}

// TestResolveAutoCloseTodoCompleted: completed → "done" in todo's
// enum [open, in_progress, done].
func TestResolveAutoCloseTodoCompleted(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "tasks", "todo")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "ship feature",
		"status": "open",
	})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got, ok := resp.AutoClosedStatus["tasks"]; !ok || got != "done" {
		t.Errorf("AutoClosedStatus[tasks] = (%q, %v), want (done, true)", got, ok)
	}
	if status, _ := readItemField(t, a, itemID, "status"); status != "done" {
		t.Errorf("item.status = %q after resolve, want done", status)
	}
}

// TestResolveAutoCloseReadingListCompleted: completed → "finished"
// in reading-list's enum [queued, reading, finished, abandoned].
func TestResolveAutoCloseReadingListCompleted(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "books", "reading-list")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "Some Book",
		"status": "reading",
	})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got, ok := resp.AutoClosedStatus["books"]; !ok || got != "finished" {
		t.Errorf("AutoClosedStatus[books] = (%q, %v), want (finished, true)", got, ok)
	}
	if status, _ := readItemField(t, a, itemID, "status"); status != "finished" {
		t.Errorf("item.status = %q after resolve, want finished", status)
	}
}

// TestResolveAutoCloseBacklogAbandoned: abandoned → "abandoned" in
// backlog's enum.
func TestResolveAutoCloseBacklogAbandoned(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "dev", "backlog")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "feature we won't do",
		"status": "open",
	})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "abandoned"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got, ok := resp.AutoClosedStatus["dev"]; !ok || got != "abandoned" {
		t.Errorf("AutoClosedStatus[dev] = (%q, %v), want (abandoned, true)", got, ok)
	}
}

// TestResolveAutoCloseTodoAbandonedFallsThrough: todo's enum is
// [open, in_progress, done]; abandoned has no match -> warning, no
// status flip, status stays "open".
func TestResolveAutoCloseTodoAbandonedFallsThrough(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "tasks", "todo")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "task to abandon",
		"status": "open",
	})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "abandoned"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if _, ok := resp.AutoClosedStatus["tasks"]; ok {
		t.Errorf("AutoClosedStatus[tasks] should be empty (no abandoned-equivalent in todo enum)")
	}
	if resp.CollectionWarning == "" {
		t.Errorf("expected CollectionWarning when no closed-equivalent exists")
	}
	if status, _ := readItemField(t, a, itemID, "status"); status != "open" {
		t.Errorf("item.status = %q after resolve, want unchanged 'open'", status)
	}
}

// TestResolveAutoCloseShoppingListFallsThrough: shopping-list has no
// status field at all; warning should fire and no field change.
func TestResolveAutoCloseShoppingListFallsThrough(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "groceries", "shopping-list")
	itemID := newCollItem(t, a, collID, map[string]any{"title": "milk"})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(resp.AutoClosedStatus) != 0 {
		t.Errorf("AutoClosedStatus = %+v, want empty (shopping-list has no status field)", resp.AutoClosedStatus)
	}
	if resp.CollectionWarning == "" {
		t.Errorf("expected CollectionWarning for shopping-list (no status field)")
	}
}

// TestResolveAutoClosePackingListFallsThrough: packing-list has
// `packed: boolean` -- not an enum, so the heuristic must NOT match
// (we don't auto-flip booleans without explicit semantics).
func TestResolveAutoClosePackingListFallsThrough(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "trip", "packing-list")
	itemID := newCollItem(t, a, collID, map[string]any{"title": "socks"})

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(resp.AutoClosedStatus) != 0 {
		t.Errorf("AutoClosedStatus = %+v, want empty (packing-list status is bool, not enum)", resp.AutoClosedStatus)
	}
	if resp.CollectionWarning == "" {
		t.Errorf("expected CollectionWarning for packing-list (no enum status field)")
	}
}

// TestResolveAutoCloseOptOut: auto_close_collection_status=false
// suppresses the flip even when the schema would match. The
// collection still appears in the warning so the caller knows to
// follow up.
func TestResolveAutoCloseOptOut(t *testing.T) {
	a, _ := setupTestAPI(t)
	collID := newCollFromTemplate(t, a, "dev", "backlog")
	itemID := newCollItem(t, a, collID, map[string]any{
		"title":  "kept open intentionally",
		"status": "open",
	})

	optOut := false
	resp, err := a.Resolve(context.Background(), ResolveRequest{
		ID:                        itemID,
		Resolution:                "completed",
		AutoCloseCollectionStatus: &optOut,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(resp.AutoClosedStatus) != 0 {
		t.Errorf("AutoClosedStatus = %+v, want empty when opt-out is set", resp.AutoClosedStatus)
	}
	if resp.CollectionWarning == "" {
		t.Errorf("expected CollectionWarning when opt-out skips the flip")
	}
	if status, _ := readItemField(t, a, itemID, "status"); status != "open" {
		t.Errorf("item.status = %q after opt-out resolve, want unchanged 'open'", status)
	}
}

// TestResolveAutoCloseNotInCollection: a Memory record outside any
// collection resolves cleanly with no warnings, no auto-close map.
func TestResolveAutoCloseNotInCollection(t *testing.T) {
	a, _ := setupTestAPI(t)

	cap, capErr := a.Save(context.Background(), SaveRequest{Content: "a memory"})
	if capErr != nil {
		t.Fatalf("Capture: %v", capErr)
	}

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: cap.ID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resp.AutoClosedStatus) != 0 {
		t.Errorf("AutoClosedStatus = %+v, want empty (record not in a collection)", resp.AutoClosedStatus)
	}
	if resp.CollectionWarning != "" {
		t.Errorf("expected no warning for non-collection record; got %q", resp.CollectionWarning)
	}
}

// TestResolveAutoCloseMultipleCollectionsMixed: a record in two
// collections -- one with a matching status field, one without.
// AutoClosedStatus contains only the matching collection; the other
// shows up in the warning.
func TestResolveAutoCloseMultipleCollectionsMixed(t *testing.T) {
	a, _ := setupTestAPI(t)

	// Create a backlog (has status enum).
	devID := newCollFromTemplate(t, a, "dev", "backlog")
	// Create a shopping-list (no status field).
	shopID := newCollFromTemplate(t, a, "groceries", "shopping-list")

	// Add an item to the backlog.
	itemID := newCollItem(t, a, devID, map[string]any{
		"title":  "buy a thing",
		"status": "open",
	})

	// Also link it into the shopping-list directly via CollectionMove
	// is wrong (move = unlink + link). Use the engine to add a second
	// member_of edge for this test.
	a.engine.Lock()
	if _, err := a.engine.Graph().AddEdge(itemID, shopID, "member_of", 1.0, nil); err != nil {
		a.engine.Unlock()
		t.Fatalf("AddEdge member_of shopping: %v", err)
	}
	if _, err := a.engine.Save("test seed"); err != nil {
		a.engine.Unlock()
		t.Fatalf("Save: %v", err)
	}
	a.engine.Unlock()

	resp, err := a.Resolve(context.Background(), ResolveRequest{ID: itemID, Resolution: "completed"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got, ok := resp.AutoClosedStatus["dev"]; !ok || got != "resolved" {
		t.Errorf("AutoClosedStatus[dev] = (%q, %v), want (resolved, true)", got, ok)
	}
	if _, ok := resp.AutoClosedStatus["groceries"]; ok {
		t.Errorf("AutoClosedStatus must NOT include 'groceries' (no status field)")
	}
	if resp.CollectionWarning == "" {
		t.Errorf("expected warning naming 'groceries' (the unflipped collection)")
	}
}

// TestInferClosedStatus_Cases is the unit-level test for the
// heuristic that resolve.go uses. Pinned separately so future schema
// vocabulary changes show up clearly without going through the API.
func TestInferClosedStatusCases(t *testing.T) {
	cases := []struct {
		name       string
		schema     *CollectionSchema
		resolution string
		want       string
	}{
		{
			name: "backlog completed",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "in_progress", "resolved", "abandoned"}},
			}},
			resolution: "completed",
			want:       "resolved",
		},
		{
			name: "backlog abandoned",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "in_progress", "resolved", "abandoned"}},
			}},
			resolution: "abandoned",
			want:       "abandoned",
		},
		{
			name: "todo completed",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "in_progress", "done"}},
			}},
			resolution: "completed",
			want:       "done",
		},
		{
			name: "todo abandoned has no match",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "in_progress", "done"}},
			}},
			resolution: "abandoned",
			want:       "",
		},
		{
			name: "reading-list completed",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"queued", "reading", "finished", "abandoned"}},
			}},
			resolution: "completed",
			want:       "finished",
		},
		{
			name: "boolean status field is not auto-flipped",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeBoolean},
			}},
			resolution: "completed",
			want:       "",
		},
		{
			name: "case-insensitive field name match",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "Status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
			}},
			resolution: "completed",
			want:       "done",
		},
		{
			name: "case-insensitive enum value match",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"Open", "Done"}},
			}},
			resolution: "completed",
			want:       "Done",
		},
		{
			name: "no status field returns empty",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "title", Type: FieldTypeString},
			}},
			resolution: "completed",
			want:       "",
		},
		{
			name:       "nil schema returns empty",
			schema:     nil,
			resolution: "completed",
			want:       "",
		},
		{
			name: "superseded uses positive vocabulary",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "resolved", "abandoned"}},
			}},
			resolution: "superseded",
			want:       "resolved",
		},
		{
			name: "obsolete uses abandoned vocabulary",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "resolved", "abandoned"}},
			}},
			resolution: "obsolete",
			want:       "abandoned",
		},
		{
			name: "unknown resolution returns empty",
			schema: &CollectionSchema{Fields: []SchemaField{
				{Name: "status", Type: FieldTypeEnum, Values: []string{"open", "done"}},
			}},
			resolution: "garbage",
			want:       "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inferClosedStatus(c.schema, c.resolution)
			if got != c.want {
				t.Errorf("inferClosedStatus(_, %q) = %q, want %q", c.resolution, got, c.want)
			}
		})
	}
}
