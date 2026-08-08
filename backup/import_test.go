package backup

import (
	"strings"
	"testing"
)

func TestImportJSONRoundTrip(t *testing.T) {
	eng := setupTestEngine(t)

	// Create records, export, import into fresh engine.
	id1 := addTestRecord(t, eng, "Record one", "One", "durable", 0.9, []string{"test"})
	id2 := addTestRecord(t, eng, "Record two", "Two", "temporal", 0.7, []string{"test"})

	// Create an edge between them.
	eng.Lock()
	eng.Graph().AddEdge(id1, id2, "related_to", 0.8, nil)
	eng.Save("link")
	eng.Unlock()

	// Export.
	var buf strings.Builder
	exportAll(&buf, eng, "jsonl")

	// Import into fresh engine.
	eng2 := setupTestEngine(t)
	result, err := ImportJSON(strings.NewReader(buf.String()), eng2, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}

	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	// Verify records exist with new IDs.
	eng2.RLock()
	defer eng2.RUnlock()
	ids := eng2.Graph().AllNodeIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(ids))
	}

	// Verify original IDs stored as imported_from_id.
	for _, nid := range ids {
		n, _ := eng2.Graph().GetNode(nid)
		origID, ok := n.Properties.GetString("imported_from_id")
		if !ok {
			t.Fatal("imported_from_id not set")
		}
		if origID != id1 && origID != id2 {
			t.Fatalf("unexpected imported_from_id: %s", origID)
		}
	}
}

func TestImportJSONPropertyAllowlist(t *testing.T) {
	eng := setupTestEngine(t)

	// Try to import a record with system properties that should be rejected.
	input := `{"id":"old-id","properties":{"content_full":"test content","processing_status":"processed","embedding_full":"should-be-rejected","activation_boost":"should-be-rejected","temporality":"durable"}}`

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d (skipped=%d, errors=%d, warnings=%v)",
			result.Imported, result.Skipped, result.Errors, result.Warnings)
	}

	// Verify only safe properties were set.
	eng.RLock()
	defer eng.RUnlock()
	ids := eng.Graph().AllNodeIDs()
	n, _ := eng.Graph().GetNode(ids[0])

	// processing_status should be "captured" (not "processed" from input).
	ps, _ := n.Properties.GetString("processing_status")
	if ps != "captured" {
		t.Fatalf("processing_status should be 'captured', got %q", ps)
	}

	// embedding_full should not exist.
	if _, ok := n.Properties["embedding_full"]; ok {
		t.Fatal("embedding_full should not be imported")
	}

	// activation_boost should not exist.
	if _, ok := n.Properties["activation_boost"]; ok {
		t.Fatal("activation_boost should not be imported")
	}

	// temporality should be set (safe property).
	temp, _ := n.Properties.GetString("temporality")
	if temp != "durable" {
		t.Fatalf("temporality should be 'durable', got %q", temp)
	}
}

func TestImportJSONMaxRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: imports 10k records through bbolt (~40s)")
	}
	eng := setupTestEngine(t)

	// Build input with more than maxImportRecords.
	var lines []string
	for i := 0; i < maxImportRecords+10; i++ {
		lines = append(lines, `{"id":"id","properties":{"content_full":"test"}}`)
	}
	input := strings.Join(lines, "\n")

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}

	if result.Imported > maxImportRecords {
		t.Fatalf("imported %d records, should cap at %d", result.Imported, maxImportRecords)
	}
}

func TestImportJSONInvalidUTF8(t *testing.T) {
	eng := setupTestEngine(t)

	// Content with invalid UTF-8.
	input := `{"id":"id","properties":{"content_full":"valid start \xff invalid end"}}`

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error for invalid UTF-8, got %d", result.Errors)
	}
	if result.Imported != 0 {
		t.Fatalf("expected 0 imported, got %d", result.Imported)
	}
}

func TestImportJSONContentLengthLimit(t *testing.T) {
	eng := setupTestEngine(t)

	bigContent := strings.Repeat("x", 200)
	input := `{"id":"id","properties":{"content_full":"` + bigContent + `"}}`

	// Max content = 100, should reject.
	result, err := ImportJSON(strings.NewReader(input), eng, 100)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("expected 1 error for content too long, got %d", result.Errors)
	}
}

func TestImportJSONEdgesOnlyWithinBatch(t *testing.T) {
	eng := setupTestEngine(t)

	// Pre-existing record.
	existingID := addTestRecord(t, eng, "Existing", "Existing", "durable", 0.9, nil)

	// Import with edge pointing to pre-existing record (should be skipped).
	input := `{"id":"new-1","properties":{"content_full":"new record"},"edges":[{"id":"e1","target_id":"` + existingID + `","edge_type":"related_to","edge_weight":0.5,"direction":"outbound"}]}`

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", result.Imported)
	}

	// Verify no edge to pre-existing record.
	eng.RLock()
	defer eng.RUnlock()
	for _, nid := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(nid)
		if origID, ok := n.Properties.GetString("imported_from_id"); ok && origID == "new-1" {
			edges := eng.Graph().EdgesFrom(nid)
			if len(edges) != 0 {
				t.Fatal("should not create edges to pre-existing records")
			}
		}
	}
}

func TestImportJSONEmptyContent(t *testing.T) {
	eng := setupTestEngine(t)

	input := `{"id":"id","properties":{"content_full":""}}`
	result, _ := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if result.Imported != 0 {
		t.Fatalf("expected 0 imported for empty content, got %d", result.Imported)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", result.Skipped)
	}
}

func TestImportCSVBasic(t *testing.T) {
	eng := setupTestEngine(t)

	csvData := `title,content,tags,temporality
"My Note","This is the full content","tag1;tag2","durable"
"Another","More content","tag3","temporal"
`

	result, err := ImportCSV(strings.NewReader(csvData), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	eng.RLock()
	defer eng.RUnlock()
	if eng.Graph().NodeCount() != 2 {
		t.Fatalf("expected 2 nodes, got %d", eng.Graph().NodeCount())
	}
}

func TestImportCSVColumnAliases(t *testing.T) {
	eng := setupTestEngine(t)

	// Use aliased column names.
	csvData := `name,body,source,date
"Test","Content here","http://example.com","2026-04-01T00:00:00Z"
`
	result, err := ImportCSV(strings.NewReader(csvData), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", result.Imported)
	}

	eng.RLock()
	defer eng.RUnlock()
	ids := eng.Graph().AllNodeIDs()
	n, _ := eng.Graph().GetNode(ids[0])

	// "body" should map to content_full.
	content, _ := n.Properties.GetString("content_full")
	if content != "Content here" {
		t.Fatalf("expected 'Content here', got %q", content)
	}

	// "name" should map to content_short.
	short, _ := n.Properties.GetString("content_short")
	if short != "Test" {
		t.Fatalf("expected 'Test', got %q", short)
	}
}

func TestParseFrontmatter(t *testing.T) {
	input := `---
tags: [meeting, project-x]
date: 2026-03-15
---

# Meeting Notes

Discussed architecture.`

	body, tags := parseFrontmatter(input)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d: %v", len(tags), tags)
	}
	if tags[0] != "meeting" || tags[1] != "project-x" {
		t.Fatalf("unexpected tags: %v", tags)
	}
	if !strings.Contains(body, "Meeting Notes") {
		t.Fatal("body should contain content")
	}
	if strings.Contains(body, "tags:") {
		t.Fatal("body should not contain frontmatter")
	}
}

func TestParseFrontmatterListFormat(t *testing.T) {
	input := `---
tags:
  - alpha
  - beta
  - gamma
---

Content here.`

	_, tags := parseFrontmatter(input)
	if len(tags) != 3 {
		t.Fatalf("expected 3 tags, got %d: %v", len(tags), tags)
	}
	if tags[0] != "alpha" || tags[2] != "gamma" {
		t.Fatalf("unexpected tags: %v", tags)
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	input := "Just plain content."
	body, tags := parseFrontmatter(input)
	if body != input {
		t.Fatalf("body should be unchanged, got %q", body)
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tags, got %v", tags)
	}
}

func TestExtractWikilinks(t *testing.T) {
	input := `See [[Architecture Decision]] and [[API Design|the API]].
Also related to [[Architecture Decision]] again.`

	links := extractWikilinks(input)
	if len(links) != 2 {
		t.Fatalf("expected 2 unique links, got %d: %v", len(links), links)
	}
	found := map[string]bool{}
	for _, l := range links {
		found[l] = true
	}
	if !found["Architecture Decision"] {
		t.Fatal("missing 'Architecture Decision'")
	}
	if !found["API Design"] {
		t.Fatal("missing 'API Design'")
	}
}

func TestBuildSafePropsRejectsSystemProps(t *testing.T) {
	raw := map[string]any{
		"content_full":      "safe",
		"temporality":       "durable",
		"processing_status": "processed", // unsafe
		"embedding_full":    "vector",    // unsafe
		"activation_boost":  "1.0",       // unsafe
		"access_count":      "5",         // not in allowlist
	}

	props := buildSafeProps(raw)

	if _, ok := props["content_full"]; !ok {
		t.Fatal("content_full should be allowed")
	}
	if _, ok := props["temporality"]; !ok {
		t.Fatal("temporality should be allowed")
	}
	if _, ok := props["processing_status"]; ok {
		t.Fatal("processing_status should be blocked")
	}
	if _, ok := props["embedding_full"]; ok {
		t.Fatal("embedding_full should be blocked")
	}
	if _, ok := props["activation_boost"]; ok {
		t.Fatal("activation_boost should be blocked")
	}
	if _, ok := props["access_count"]; ok {
		t.Fatal("access_count should be blocked")
	}
}

// TestImportCSVPreservesContentShort: gramaton's own CSV export
// writes content_short under the header summary_short; the importer
// must map that column back, so a CSV export/import round trip keeps
// every record's summary. Regression test for the alias gap where
// the column was silently dropped (issue #111).
func TestImportCSVPreservesContentShort(t *testing.T) {
	src := setupTestEngine(t)
	id := addTestRecord(t, src, "Round trip content", "The original summary", "durable", 0.9, nil)

	var buf strings.Builder
	if err := StreamRecords(&buf, src, "csv", []string{id}); err != nil {
		t.Fatalf("StreamRecords csv: %v", err)
	}
	if !strings.Contains(buf.String(), "summary_short") {
		t.Fatalf("CSV export header lost the summary_short column:\n%s", buf.String())
	}

	dst := setupTestEngine(t)
	result, err := ImportCSV(strings.NewReader(buf.String()), dst, 1024*1024)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", result.Imported)
	}

	dst.RLock()
	defer dst.RUnlock()
	found := false
	for _, nid := range dst.Graph().AllNodeIDs() {
		n, _ := dst.Graph().GetNode(nid)
		if content, _ := n.Properties.GetString("content_full"); content != "Round trip content" {
			continue
		}
		found = true
		if short, _ := n.Properties.GetString("content_short"); short != "The original summary" {
			t.Errorf("content_short = %q, want %q preserved across the CSV round trip", short, "The original summary")
		}
	}
	if !found {
		t.Fatal("imported record not found in destination store")
	}
}
