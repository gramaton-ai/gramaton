package backup

import (
	"strings"
	"testing"
)

func TestBackupEmptyStore(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Create minimal store structure.
	writeFile(t, dataDir+"/HEAD", "")

	archivePath, err := Create(dataDir, "", backupDir)
	if err != nil {
		t.Fatalf("Create empty store: %v", err)
	}

	// Should produce a valid archive.
	if !IsBackupArchive(archivePath) {
		t.Fatal("should be recognized as backup archive")
	}
}

func TestExportEmptyStoreCSV(t *testing.T) {
	eng := setupTestEngine(t)
	var buf strings.Builder
	eng.RLock()
	err := ExportCSV(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportCSV empty: %v", err)
	}
	// Should have header only.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header), got %d", len(lines))
	}
}

func TestExportEmptyStoreMarkdown(t *testing.T) {
	eng := setupTestEngine(t)
	var buf strings.Builder
	eng.RLock()
	err := ExportMarkdown(&buf, eng)
	eng.RUnlock()

	if err != nil {
		t.Fatalf("ExportMarkdown empty: %v", err)
	}
	if !strings.Contains(buf.String(), "Exported 0 records") {
		t.Fatal("should say 0 records")
	}
}

func TestImportJSONNoEdgesToExisting(t *testing.T) {
	eng := setupTestEngine(t)

	// Create an existing record.
	existingID := addTestRecord(t, eng, "Existing", "Existing", "durable", 0.9, nil)

	// Import a record that tries to edge to the existing one.
	input := `{"id":"new-1","properties":{"content_full":"New record"},"edges":[{"id":"e1","target_id":"` + existingID + `","edge_type":"related_to","edge_weight":0.5,"direction":"outbound"}]}`

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}

	// Record imported but edge should NOT be created (target not in batch).
	if result.Imported != 1 {
		t.Fatalf("expected 1 imported, got %d", result.Imported)
	}

	// Verify no edge to existing record.
	eng.RLock()
	defer eng.RUnlock()
	for _, id := range eng.Graph().AllNodeIDs() {
		n, _ := eng.Graph().GetNode(id)
		if _, ok := n.Properties.GetString("imported_from_id"); ok {
			edges := eng.Graph().EdgesFrom(id)
			for _, e := range edges {
				if e.TargetID == existingID {
					t.Fatal("should not create edges to pre-existing records")
				}
			}
		}
	}
}

func TestImportJSONEdgesWithinBatch(t *testing.T) {
	eng := setupTestEngine(t)

	input := `{"id":"a","properties":{"content_full":"Record A"}}
{"id":"b","properties":{"content_full":"Record B"},"edges":[{"id":"e1","target_id":"a","edge_type":"related_to","edge_weight":0.7,"direction":"outbound"}]}`

	result, err := ImportJSON(strings.NewReader(input), eng, 1024*1024)
	if err != nil {
		t.Fatalf("ImportJSON: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d", result.Imported)
	}

	// Verify edge exists between the two imported records.
	eng.RLock()
	defer eng.RUnlock()
	edgeFound := false
	for _, id := range eng.Graph().AllNodeIDs() {
		for _, e := range eng.Graph().EdgesFrom(id) {
			if e.Type == "related_to" {
				edgeFound = true
			}
		}
	}
	if !edgeFound {
		t.Fatal("expected edge between imported records")
	}
}

func TestRetentionExactCount(t *testing.T) {
	dir := t.TempDir()

	// Create exactly retain count.
	for i := 0; i < 2; i++ {
		writeFile(t, dir+"/gramaton-backup-"+string(rune('a'+i))+".tar.gz", "data")
	}

	deleted, err := ApplyRetention(dir, 2)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("should not delete when exactly at retain count, deleted %d", len(deleted))
	}
}
