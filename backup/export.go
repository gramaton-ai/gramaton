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
// Caller must hold at least a read lock.
func gatherRecords(e *core.Engine) []ExportRecord {
	g := e.Graph()
	var records []ExportRecord

	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		id := n.ID

		// Skip chunks and deleted records.
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
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

		records = append(records, rec)
	}

	return records
}

// ExportJSON writes records as JSON Lines (one JSON object per line).
// Caller must hold at least a read lock on the engine.
func ExportJSON(w io.Writer, e *core.Engine) error {
	records := gatherRecords(e)
	enc := json.NewEncoder(w)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("encode record %s: %w", rec.ID, err)
		}
	}
	return nil
}

// ExportCSV writes records as CSV with a header row.
// Caller must hold at least a read lock on the engine.
func ExportCSV(w io.Writer, e *core.Engine) error {
	records := gatherRecords(e)
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header.
	header := []string{
		"id", "summary_short", "content_full", "temporality",
		"confidence", "knowledge_type", "epistemic_status",
		"importance", "keywords", "created_at", "valid_from",
		"valid_until", "source_ref",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, rec := range records {
		row := []string{
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
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	return cw.Error()
}

// ExportMarkdown writes records as human-readable markdown.
// Caller must hold at least a read lock on the engine.
func ExportMarkdown(w io.Writer, e *core.Engine) error {
	records := gatherRecords(e)

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
