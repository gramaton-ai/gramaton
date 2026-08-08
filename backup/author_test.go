package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/testutil"
)

// The identity configured on the importing engine. Import must never
// stamp this onto JSONL/CSV rows; Obsidian import must stamp it on
// every note.
const importerAuthor = "Importer Identity <importer@example.com>"

// setupAuthorEngine mirrors setupTestEngine with an author identity
// configured, for exercising import-side author behavior.
func setupAuthorEngine(t *testing.T) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.Author = config.AuthorConfig{Name: "Importer Identity", Email: "importer@example.com"}
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	testutil.RegisterEngineCleanup(t, eng)
	return eng
}

// TestImportJSONPreservesAuthor: an exported record's author survives
// the JSONL round trip verbatim, a record without an author stays
// author-less, and the importing engine's own configured identity is
// never stamped on either.
func TestImportJSONPreservesAuthor(t *testing.T) {
	src := setupTestEngine(t)
	const originalAuthor = "Original Author <orig@example.com>"

	src.Lock()
	src.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("record with an author"),
		"author":       graph.StringProperty(originalAuthor),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	})
	src.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("record without an author"),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	})
	src.Save("seed")
	src.Unlock()

	var buf strings.Builder
	src.RLock()
	if err := exportAll(&buf, src, "jsonl"); err != nil {
		src.RUnlock()
		t.Fatalf("ExportJSONL: %v", err)
	}
	src.RUnlock()

	// The destination engine HAS an author configured; JSONL import
	// must ignore it and preserve (or omit) the source rows' authors.
	dst := setupAuthorEngine(t)
	result, err := ImportJSON(strings.NewReader(buf.String()), dst, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	dst.RLock()
	defer dst.RUnlock()
	checked := 0
	for _, id := range dst.Graph().AllNodeIDs() {
		n, _ := dst.Graph().GetNode(id)
		content, _ := n.Properties.GetString("content_full")
		author, hasAuthor := n.Properties.GetString("author")
		switch content {
		case "record with an author":
			checked++
			if !hasAuthor || author != originalAuthor {
				t.Errorf("author = %q (present=%v), want preserved %q", author, hasAuthor, originalAuthor)
			}
		case "record without an author":
			checked++
			if hasAuthor {
				t.Errorf("author-less row gained author %q on import; must stay absent", author)
			}
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d of 2 imported records", checked)
	}
}

// TestImportCSVPreservesAuthor: the CSV export carries the author
// column and import preserves it verbatim across the round trip.
// Rows without an author stay author-less, and the importing
// engine's own configured identity is never stamped on either.
// Exercises StreamRecords (the api.BackupExport production path);
// writeRecordsCSV emits the same columns by construction (both
// writers share csvHeader/csvRow).
func TestImportCSVPreservesAuthor(t *testing.T) {
	src := setupTestEngine(t)
	const originalAuthor = "Alice <alice@example.com>"

	src.Lock()
	src.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("record with an author"),
		"author":       graph.StringProperty(originalAuthor),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	})
	src.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("record without an author"),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	})
	src.Save("seed")
	src.Unlock()

	src.RLock()
	ids := src.Graph().AllNodeIDs()
	src.RUnlock()

	// StreamRecords takes its own per-record read lock.
	var buf strings.Builder
	if err := StreamRecords(&buf, src, "csv", ids); err != nil {
		t.Fatalf("StreamRecords csv: %v", err)
	}

	// The destination engine HAS an author configured; CSV import
	// must ignore it and preserve (or omit) the source rows' authors.
	dst := setupAuthorEngine(t)
	result, err := ImportCSV(strings.NewReader(buf.String()), dst, 1024*1024)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	dst.RLock()
	defer dst.RUnlock()
	checked := 0
	for _, id := range dst.Graph().AllNodeIDs() {
		n, _ := dst.Graph().GetNode(id)
		content, _ := n.Properties.GetString("content_full")
		author, hasAuthor := n.Properties.GetString("author")
		switch content {
		case "record with an author":
			checked++
			if !hasAuthor || author != originalAuthor {
				t.Errorf("author = %q (present=%v), want preserved %q", author, hasAuthor, originalAuthor)
			}
		case "record without an author":
			checked++
			if hasAuthor {
				t.Errorf("author-less row gained author %q on import; must stay absent", author)
			}
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d of 2 imported records", checked)
	}
}

// TestImportObsidianStampsAuthor: a vault import is the user
// ingesting their own notes, so every imported note carries the
// configured author identity.
func TestImportObsidianStampsAuthor(t *testing.T) {
	vault := t.TempDir()
	notes := map[string]string{
		"first.md":  "# First\n\nA note about the first thing.",
		"second.md": "# Second\n\nA note that links to [[first]].",
	}
	for name, content := range notes {
		if err := os.WriteFile(filepath.Join(vault, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	eng := setupAuthorEngine(t)
	result, err := ImportObsidian(vault, eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportObsidian: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		author, ok := n.Properties.GetString("author")
		if !ok {
			t.Errorf("imported note %s has no author property, want %q", id, importerAuthor)
			continue
		}
		if author != importerAuthor {
			t.Errorf("imported note %s author = %q, want %q", id, author, importerAuthor)
		}
	}
}
