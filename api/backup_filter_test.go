package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/backup"
)

// captureForExport writes a Memory record with a marker substring
// so filtered exports can pick it out.
func captureForExport(t *testing.T, a *API, marker string) string {
	t.Helper()
	conf := 0.9
	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:         marker + " body content for export filter test",
		SummaryShort:    marker + " summary",
		Temporality:     "durable",
		Confidence:      &conf,
		KnowledgeType:   "semantic",
		EpistemicStatus: "well_established",
	})
	if apiErr != nil {
		t.Fatalf("capture: %v", apiErr)
	}
	return resp.ID
}

// TestBackupExportUnfilteredFullDump pins back-compat: an empty
// ExportRequest still dumps every record (the pre-PR-C behavior).
// This is the "no filter" branch in collectExportIDs.
func TestBackupExportUnfilteredFullDump(t *testing.T) {
	a, _ := setupTestAPI(t)
	captureForExport(t, a, "exportzzz1")
	captureForExport(t, a, "exportzzz2")

	var buf bytes.Buffer
	ct, apiErr := a.BackupExport(context.Background(), ExportRequest{Format: "jsonl"}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport: %v", apiErr)
	}
	if ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	// Both records should be in the dump.
	body := buf.String()
	for _, want := range []string{"exportzzz1", "exportzzz2"} {
		if !strings.Contains(body, want) {
			t.Errorf("unfiltered export missing %q in body", want)
		}
	}
}

// TestBackupExportMatchFiltersRecords pins the filter-aware path:
// passing Match narrows the export to only matching records.
// Without filter wiring (pre-PR-C) this would fail because the CLI
// flags pretended to filter but the server ignored them.
func TestBackupExportMatchFiltersRecords(t *testing.T) {
	a, _ := setupTestAPI(t)
	captureForExport(t, a, "alphazzzkeep")
	captureForExport(t, a, "betazzzdrop")
	captureForExport(t, a, "gammazzzkeep")

	var buf bytes.Buffer
	_, apiErr := a.BackupExport(context.Background(), ExportRequest{
		Format: "jsonl",
		Match:  "zzzkeep",
	}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport: %v", apiErr)
	}
	body := buf.String()
	for _, want := range []string{"alphazzzkeep", "gammazzzkeep"} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered export missing %q", want)
		}
	}
	if strings.Contains(body, "betazzzdrop") {
		t.Error("filtered export includes record that shouldn't match")
	}
}

// TestBackupExportJSONArrayFormat pins the new "json" semantic:
// produces a parseable JSON array, not JSONL. Distinct shape from
// "jsonl" which streams line-by-line.
func TestBackupExportJSONArrayFormat(t *testing.T) {
	a, _ := setupTestAPI(t)
	captureForExport(t, a, "arrayformatzzz")

	var buf bytes.Buffer
	ct, apiErr := a.BackupExport(context.Background(), ExportRequest{Format: "json"}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport: %v", apiErr)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var arr []backup.ExportRecord
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("body should parse as JSON array, but didn't: %v\nbody: %q", err, buf.String())
	}
	if len(arr) == 0 {
		t.Error("expected at least 1 record in array")
	}
}

// TestBackupExportEmptyResultEmptyArray pins the contract for the
// JSON array format on an empty result set: produces "[]", not
// some other empty shape.
func TestBackupExportEmptyResultEmptyArray(t *testing.T) {
	a, _ := setupTestAPI(t)
	// Don't capture anything; query something that won't match.

	var buf bytes.Buffer
	_, apiErr := a.BackupExport(context.Background(), ExportRequest{
		Format: "json",
		Match:  "zzznevermatchesanythingzzz",
	}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport: %v", apiErr)
	}
	got := strings.TrimSpace(buf.String())
	if got != "[]" {
		t.Errorf("empty filtered export: got %q, want \"[]\"", got)
	}
}

// TestBackupExportDefaultFormatIsJSONL pins the default-format
// behavior: when Format is omitted, the export is JSONL
// (matches today's CLI experience where `--format` defaulted to
// "json" but produced JSONL; new default `jsonl` matches the
// shape).
func TestBackupExportDefaultFormatIsJSONL(t *testing.T) {
	a, _ := setupTestAPI(t)
	captureForExport(t, a, "defaultformatzzz")

	var buf bytes.Buffer
	ct, apiErr := a.BackupExport(context.Background(), ExportRequest{}, &buf)
	if apiErr != nil {
		t.Fatalf("BackupExport: %v", apiErr)
	}
	if ct != "application/x-ndjson" {
		t.Errorf("default Content-Type = %q, want application/x-ndjson (JSONL)", ct)
	}
}
