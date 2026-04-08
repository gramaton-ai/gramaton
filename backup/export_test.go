package backup

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

func setupTestEngine(t *testing.T) *core.Engine {
	t.Helper()
	// Use core's test helpers to create a minimal engine.
	// Since core.LoadEngine needs filesystem, create a temp dir.
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = "" // no embedder for tests

	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}

	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	return eng
}

func addTestRecord(t *testing.T, eng *core.Engine, content, summary, temporality string, confidence float64, keywords []string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()

	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"content_short":     graph.StringProperty(summary),
		"temporality":       graph.StringProperty(temporality),
		"confidence":        graph.Float64Property(confidence),
		"knowledge_type":    graph.StringProperty("semantic"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	if len(keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(keywords)
	}

	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("test")
	return n.ID
}

func TestExportJSON(t *testing.T) {
	eng := setupTestEngine(t)
	id := addTestRecord(t, eng, "Test content", "Test summary", "durable", 0.9, []string{"test", "export"})

	var buf bytes.Buffer
	eng.RLock()
	err := ExportJSON(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}

	// Parse JSON Lines.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var rec ExportRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if rec.ID != id {
		t.Fatalf("expected ID %s, got %s", id, rec.ID)
	}
	if rec.Properties["content_full"] != "Test content" {
		t.Fatalf("expected 'Test content', got %v", rec.Properties["content_full"])
	}
	if rec.Properties["temporality"] != "durable" {
		t.Fatalf("expected 'durable', got %v", rec.Properties["temporality"])
	}
}

func TestExportJSONExcludesEmbeddings(t *testing.T) {
	eng := setupTestEngine(t)
	addTestRecord(t, eng, "Test", "Test", "durable", 0.9, nil)

	// Manually add an embedding property.
	eng.Lock()
	ids := eng.Graph().AllNodeIDs()
	if len(ids) > 0 {
		eng.Graph().SetNodeProperty(ids[0], "embedding_full",
			graph.VectorProperty([]float32{0.1, 0.2, 0.3}))
	}
	eng.Unlock()

	var buf bytes.Buffer
	eng.RLock()
	ExportJSON(&buf, eng)
	eng.RUnlock()

	if strings.Contains(buf.String(), "embedding_full") {
		t.Fatal("export should not include embeddings")
	}
}

func TestExportJSONExcludesChunks(t *testing.T) {
	eng := setupTestEngine(t)
	parentID := addTestRecord(t, eng, "Parent content", "Parent", "durable", 0.9, nil)

	// Create a chunk node.
	eng.Lock()
	chunk := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("chunk text"),
	})
	eng.Graph().AddEdge(chunk.ID, parentID, "chunk_of", 1.0, nil)
	for k, v := range chunk.Properties {
		eng.PropIdx().Add(chunk.ID, k, v)
	}
	eng.Unlock()

	var buf bytes.Buffer
	eng.RLock()
	ExportJSON(&buf, eng)
	eng.RUnlock()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 record (chunk excluded), got %d", len(lines))
	}
}

func TestExportCSV(t *testing.T) {
	eng := setupTestEngine(t)
	addTestRecord(t, eng, "CSV test content", "CSV summary", "temporal", 0.7, []string{"csv", "test"})

	var buf bytes.Buffer
	eng.RLock()
	err := ExportCSV(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}

	// Header + 1 data row.
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (header + data), got %d", len(rows))
	}

	header := rows[0]
	if header[0] != "id" {
		t.Fatalf("first column should be 'id', got %q", header[0])
	}

	data := rows[1]
	// Find content_full column.
	contentIdx := -1
	for i, h := range header {
		if h == "content_full" {
			contentIdx = i
			break
		}
	}
	if contentIdx < 0 {
		t.Fatal("content_full column not found")
	}
	if data[contentIdx] != "CSV test content" {
		t.Fatalf("expected 'CSV test content', got %q", data[contentIdx])
	}
}

func TestExportMarkdown(t *testing.T) {
	eng := setupTestEngine(t)
	addTestRecord(t, eng, "Markdown test", "MD summary", "durable", 0.95, []string{"md"})

	var buf bytes.Buffer
	eng.RLock()
	err := ExportMarkdown(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "# Gramaton Export") {
		t.Fatal("missing export header")
	}
	if !strings.Contains(output, "## MD summary") {
		t.Fatal("missing record section header")
	}
	if !strings.Contains(output, "Markdown test") {
		t.Fatal("missing content")
	}
	if !strings.Contains(output, "durable") {
		t.Fatal("missing temporality")
	}
}

func TestExportEmptyStore(t *testing.T) {
	eng := setupTestEngine(t)

	var buf bytes.Buffer
	eng.RLock()
	err := ExportJSON(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("expected empty output, got %q", buf.String())
	}
}

// Suppress unused import warning for index package.
var _ = index.NewPropertyIndex
