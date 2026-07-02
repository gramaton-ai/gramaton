package backup

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// ExportRecord is a single record in the export format.
type ExportRecord struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
	Edges      []ExportEdge   `json:"edges,omitempty"`
}

// ExportEdge is an edge in the export format.
type ExportEdge struct {
	ID        string  `json:"id"`
	TargetID  string  `json:"target_id"`
	Type      string  `json:"edge_type"`
	Weight    float64 `json:"edge_weight"`
	Direction string  `json:"direction"`
}

// gatherRecords collects all exportable records from the engine.
// Caller must hold at least a read lock. Skips chunks and deleted
// records to match the engine's "live, non-internal" record set.
//
// Equivalent to GatherRecordsByIDs called with every node ID; kept
// for back-compat with callers that don't pre-filter.
func gatherRecords(e *core.Engine) []ExportRecord {
	g := e.Graph()
	var ids []string
	it := g.NodeIterator()
	for it.Next() {
		ids = append(ids, it.Node().ID)
	}
	it.Close()
	return GatherRecordsByIDs(e, ids)
}

// GatherRecordsByIDs collects ExportRecord objects for the given
// IDs in order. Records that no longer exist, are chunk nodes, or
// are tombstoned (processing_status=deleted) are silently skipped.
// Caller must hold at least a read lock for the duration; callers
// streaming records to a writer outside the lock should use
// GatherSingleRecord per-record instead.
func GatherRecordsByIDs(e *core.Engine, ids []string) []ExportRecord {
	g := e.Graph()
	out := make([]ExportRecord, 0, len(ids))
	for _, id := range ids {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
		if rec, ok := buildExportRecord(g, n); ok {
			out = append(out, rec)
		}
	}
	return out
}

// GatherSingleRecord builds an ExportRecord for a single ID under
// a brief lock window. Returns false if the node is missing,
// tombstoned, or a chunk. Used by streaming exports that release
// the engine lock between records.
func GatherSingleRecord(e *core.Engine, id string) (ExportRecord, bool) {
	g := e.Graph()
	n, ok := g.GetNode(id)
	if !ok {
		return ExportRecord{}, false
	}
	return buildExportRecord(g, n)
}

// buildExportRecord serializes a single node to ExportRecord
// shape. Skips chunks and deleted records (returns ok=false).
// Caller must hold at least a read lock.
func buildExportRecord(g graph.NodeReader, n *graph.Node) (ExportRecord, bool) {
	id := n.ID
	if isChunkNode(g, id) {
		return ExportRecord{}, false
	}
	if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
		return ExportRecord{}, false
	}

	rec := ExportRecord{
		ID:         id,
		Properties: make(map[string]any, len(n.Properties)),
	}

	for k, v := range n.Properties {
		// Skip embeddings and system fields from export.
		if strings.HasPrefix(k, "embedding_") {
			continue
		}
		if k == "activation_boost" {
			continue
		}
		rec.Properties[k] = v.FormatValue()
	}

	// Collect edges.
	for _, e := range g.EdgesFrom(id) {
		if graph.IsStructuralEdge(e.Type) {
			continue
		}
		rec.Edges = append(rec.Edges, ExportEdge{
			ID:        e.ID,
			TargetID:  e.TargetID,
			Type:      e.Type,
			Weight:    e.Weight,
			Direction: "outbound",
		})
	}
	for _, e := range g.EdgesTo(id) {
		if graph.IsStructuralEdge(e.Type) {
			continue
		}
		rec.Edges = append(rec.Edges, ExportEdge{
			ID:        e.ID,
			TargetID:  e.SourceID,
			Type:      e.Type,
			Weight:    e.Weight,
			Direction: "inbound",
		})
	}

	return rec, true
}

// ExportJSONL writes records as JSON Lines (one JSON object per
// line; content-type application/x-ndjson). Caller must hold at
// least a read lock on the engine.
//
// This is what the historical "json" format produced; the
// canonical name is now "jsonl" to match the actual shape. The
// `ExportJSON` function below produces a parseable JSON array
// (the shape consumers expecting `--format json` typically want).
func ExportJSONL(w io.Writer, e *core.Engine) error {
	records := gatherRecords(e)
	return writeRecordsJSONL(w, records)
}

// ExportJSONLByIDs is ExportJSONL filtered to a pre-collected ID
// list. Used by api.BackupExport's three-phase streaming path
// where IDs are gathered under a brief RLock and records are
// fetched + written without holding the lock across I/O.
func ExportJSONLByIDs(w io.Writer, e *core.Engine, ids []string) error {
	records := GatherRecordsByIDs(e, ids)
	return writeRecordsJSONL(w, records)
}

func writeRecordsJSONL(w io.Writer, records []ExportRecord) error {
	enc := json.NewEncoder(w)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("encode record %s: %w", rec.ID, err)
		}
	}
	return nil
}

// csvHeader is the column list for CSV export, shared by both CSV
// writers (the StreamRecords "csv" case and writeRecordsCSV) so the
// two cannot drift. csvRow produces values in the same order. Import
// maps columns by header name, so appending columns at the end is
// compatible with older consumers. The trailing "author" column
// carries record attribution so it survives an export/import round
// trip (see safePropTypes in import.go).
var csvHeader = []string{
	"id", "summary_short", "content_full", "temporality",
	"confidence", "knowledge_type", "epistemic_status",
	"importance", "keywords", "created_at", "valid_from",
	"valid_until", "source_ref", "author",
}

// csvRow builds a record's CSV row in csvHeader order.
func csvRow(rec ExportRecord) []string {
	return []string{
		rec.ID,
		propString(rec.Properties, "content_short"),
		propString(rec.Properties, "content_full"),
		propString(rec.Properties, "temporality"),
		propString(rec.Properties, "confidence"),
		propString(rec.Properties, "knowledge_type"),
		propString(rec.Properties, "epistemic_status"),
		propString(rec.Properties, "importance"),
		formatKeywords(rec.Properties),
		propString(rec.Properties, "created_at"),
		propString(rec.Properties, "valid_from"),
		propString(rec.Properties, "valid_until"),
		propString(rec.Properties, "source_ref"),
		propString(rec.Properties, "author"),
	}
}

// StreamRecords writes records for `ids` to `w` in the given
// format, taking a per-record read lock and releasing it before
// the write. Avoids holding the engine lock across I/O for the
// duration of the export. Designed for api.BackupExport's three-
// phase pattern (Phase 1: collect IDs under RLock; Phase 2: this).
//
// format must be one of "jsonl", "json", "csv", "markdown".
// Records that no longer exist (deleted between phases) are
// silently skipped. Returns the first encoding/write error.
//
// JSON-array form streams (write `[`, records with commas, `]`)
// rather than buffering — large exports don't blow up memory even
// in array mode.
func StreamRecords(w io.Writer, e *core.Engine, format string, ids []string) error {
	switch format {
	case "jsonl":
		enc := json.NewEncoder(w)
		for _, id := range ids {
			e.RLock()
			rec, ok := GatherSingleRecord(e, id)
			e.RUnlock()
			if !ok {
				continue
			}
			if err := enc.Encode(rec); err != nil {
				return fmt.Errorf("encode record %s: %w", rec.ID, err)
			}
		}
		return nil

	case "json":
		if _, err := w.Write([]byte("[")); err != nil {
			return err
		}
		needComma := false
		for _, id := range ids {
			e.RLock()
			rec, ok := GatherSingleRecord(e, id)
			e.RUnlock()
			if !ok {
				continue
			}
			if needComma {
				if _, err := w.Write([]byte(",")); err != nil {
					return err
				}
			}
			data, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("encode record %s: %w", rec.ID, err)
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			needComma = true
		}
		_, err := w.Write([]byte("]"))
		return err

	case "csv":
		cw := csv.NewWriter(w)
		if err := cw.Write(csvHeader); err != nil {
			return err
		}
		for _, id := range ids {
			e.RLock()
			rec, ok := GatherSingleRecord(e, id)
			e.RUnlock()
			if !ok {
				continue
			}
			if err := cw.Write(csvRow(rec)); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()

	case "markdown":
		fmt.Fprintf(w, "# Gramaton Export\n\n")
		fmt.Fprintf(w, "Exported at %s\n\n---\n\n", time.Now().UTC().Format(time.RFC3339))
		for _, id := range ids {
			e.RLock()
			rec, ok := GatherSingleRecord(e, id)
			e.RUnlock()
			if !ok {
				continue
			}
			writeMarkdownRecord(w, rec)
		}
		return nil

	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func writeMarkdownRecord(w io.Writer, rec ExportRecord) {
	summary := propString(rec.Properties, "content_short")
	if summary == "" {
		content := propString(rec.Properties, "content_full")
		if len(content) > 80 {
			summary = content[:80] + "..."
		} else {
			summary = content
		}
	}

	fmt.Fprintf(w, "## %s\n\n", summary)
	fmt.Fprintf(w, "**ID:** %s\n", rec.ID)

	temp := propString(rec.Properties, "temporality")
	conf := propString(rec.Properties, "confidence")
	kt := propString(rec.Properties, "knowledge_type")
	es := propString(rec.Properties, "epistemic_status")
	if temp != "" || conf != "" || kt != "" || es != "" {
		parts := []string{}
		if temp != "" {
			parts = append(parts, "**Temporality:** "+temp)
		}
		if conf != "" {
			parts = append(parts, "**Confidence:** "+conf)
		}
		if kt != "" {
			parts = append(parts, "**Type:** "+kt)
		}
		if es != "" {
			parts = append(parts, "**Status:** "+es)
		}
		fmt.Fprintf(w, "%s\n", strings.Join(parts, " | "))
	}

	kw := formatKeywords(rec.Properties)
	if kw != "" {
		fmt.Fprintf(w, "**Keywords:** %s\n", kw)
	}

	created := propString(rec.Properties, "created_at")
	if created != "" {
		fmt.Fprintf(w, "**Created:** %s\n", created)
	}

	content := propString(rec.Properties, "content_full")
	if content != "" {
		fmt.Fprintf(w, "\n%s\n", content)
	}

	fmt.Fprintf(w, "\n---\n\n")
}

// ExportJSON writes records as a single JSON array (content-type
// application/json). Buffered, suited for `jq` consumption and
// one-shot tools that expect a parseable document. Caller must
// hold at least a read lock on the engine.
//
// For very large exports prefer ExportJSONL: it streams without
// buffering the entire array and is what tools like `jq` can
// process line by line.
func ExportJSON(w io.Writer, e *core.Engine) error {
	records := gatherRecords(e)
	return writeRecordsJSONArray(w, records)
}

// ExportJSONByIDs mirrors ExportJSONLByIDs for the JSON-array form.
func ExportJSONByIDs(w io.Writer, e *core.Engine, ids []string) error {
	records := GatherRecordsByIDs(e, ids)
	return writeRecordsJSONArray(w, records)
}

func writeRecordsJSONArray(w io.Writer, records []ExportRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encode records: %w", err)
	}
	_, err = w.Write(data)
	return err
}

// ExportCSVByIDs is ExportCSV filtered to a pre-collected ID list.
func ExportCSVByIDs(w io.Writer, e *core.Engine, ids []string) error {
	return writeRecordsCSV(w, GatherRecordsByIDs(e, ids))
}

// ExportCSV writes records as CSV with a header row.
// Caller must hold at least a read lock on the engine.
func ExportCSV(w io.Writer, e *core.Engine) error {
	return writeRecordsCSV(w, gatherRecords(e))
}

func writeRecordsCSV(w io.Writer, records []ExportRecord) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write(csvHeader); err != nil {
		return err
	}

	for _, rec := range records {
		if err := cw.Write(csvRow(rec)); err != nil {
			return err
		}
	}

	return cw.Error()
}

// ExportMarkdownByIDs is ExportMarkdown filtered to a pre-collected
// ID list.
func ExportMarkdownByIDs(w io.Writer, e *core.Engine, ids []string) error {
	return writeRecordsMarkdown(w, GatherRecordsByIDs(e, ids))
}

// ExportMarkdown writes records as human-readable markdown.
// Caller must hold at least a read lock on the engine.
func ExportMarkdown(w io.Writer, e *core.Engine) error {
	return writeRecordsMarkdown(w, gatherRecords(e))
}

func writeRecordsMarkdown(w io.Writer, records []ExportRecord) error {
	fmt.Fprintf(w, "# Gramaton Export\n\n")
	fmt.Fprintf(w, "Exported %d records at %s\n\n---\n\n",
		len(records), time.Now().UTC().Format(time.RFC3339))

	for _, rec := range records {
		summary := propString(rec.Properties, "content_short")
		if summary == "" {
			content := propString(rec.Properties, "content_full")
			if len(content) > 80 {
				summary = content[:80] + "..."
			} else {
				summary = content
			}
		}

		fmt.Fprintf(w, "## %s\n\n", summary)
		fmt.Fprintf(w, "**ID:** %s\n", rec.ID)

		temp := propString(rec.Properties, "temporality")
		conf := propString(rec.Properties, "confidence")
		kt := propString(rec.Properties, "knowledge_type")
		es := propString(rec.Properties, "epistemic_status")
		if temp != "" || conf != "" || kt != "" {
			parts := []string{}
			if temp != "" {
				parts = append(parts, "**Temporality:** "+temp)
			}
			if conf != "" {
				parts = append(parts, "**Confidence:** "+conf)
			}
			if kt != "" {
				parts = append(parts, "**Type:** "+kt)
			}
			if es != "" {
				parts = append(parts, "**Status:** "+es)
			}
			fmt.Fprintf(w, "%s\n", strings.Join(parts, " | "))
		}

		kw := formatKeywords(rec.Properties)
		if kw != "" {
			fmt.Fprintf(w, "**Keywords:** %s\n", kw)
		}

		created := propString(rec.Properties, "created_at")
		if created != "" {
			fmt.Fprintf(w, "**Created:** %s\n", created)
		}

		content := propString(rec.Properties, "content_full")
		if content != "" {
			fmt.Fprintf(w, "\n%s\n", content)
		}

		fmt.Fprintf(w, "\n---\n\n")
	}

	return nil
}

func propString(props map[string]any, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func formatKeywords(props map[string]any) string {
	v, ok := props["content_keywords"]
	if !ok {
		return ""
	}
	s := fmt.Sprintf("%v", v)
	// Strip brackets from stringified slice.
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	return s
}

// isChunkNode delegates to graph.IsStructuralChild.
func isChunkNode(g graph.NodeReader, id string) bool { return g.IsStructuralChild(id) }
