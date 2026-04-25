package backup

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/strutil"
)

const maxImportRecords = 10000

// ImportResult summarizes what an import operation did.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   int      `json:"errors"`
	Warnings []string `json:"warnings,omitempty"`
}

// safePropType describes how to coerce a JSON value into a graph
// Property for a given safe import field. Single source of truth:
// adding a new safe field means one entry in safePropTypes; the
// type tag determines coercion in buildSafeProps. Previously a
// safeProperties map and a buildSafeProps switch listed the same
// fields independently, with no compile-time check that they stayed
// in sync. (Wave 7 P1-74.)
type safePropType int

const (
	propTypeString safePropType = iota
	propTypeFloat
	propTypeInt64
	propTypeStringList
	propTypeTimestamp
)

// safePropTypes is the single source of truth for fields imports
// can set. Anything not listed is rejected.
var safePropTypes = map[string]safePropType{
	"content_full":        propTypeString,
	"content_short":       propTypeString,
	"content_medium":      propTypeString,
	"source_ref":          propTypeString,
	"temporality":         propTypeString,
	"knowledge_type":      propTypeString,
	"epistemic_status":    propTypeString,
	"context_about":       propTypeString,
	"context_who":         propTypeString,
	"context_prompted":    propTypeString,
	"context_findable_by": propTypeString,
	"context_related":     propTypeString,
	"resolution":          propTypeString,
	"resolution_note":     propTypeString,
	"confidence":          propTypeFloat,
	"importance":          propTypeFloat,
	"source_credibility":  propTypeFloat,
	"testimony_hops":      propTypeInt64,
	"content_keywords":    propTypeStringList,
	"created_at":          propTypeTimestamp,
	"valid_from":          propTypeTimestamp,
	"valid_until":         propTypeTimestamp,
	"asserted_as_of":      propTypeTimestamp,
}

// safeProperties is retained as a name-only set for callers that
// want a quick "is this field safe?" check without coercion.
// Derived from safePropTypes so the two cannot drift.
var safeProperties = func() map[string]bool {
	m := make(map[string]bool, len(safePropTypes))
	for name := range safePropTypes {
		m[name] = true
	}
	return m
}()

// ImportJSON reads JSON Lines (one ExportRecord per line) and creates
// records in the store. New ULIDs are assigned; original IDs stored as
// imported_from_id. Edges are remapped and only created between records
// in the same import batch.
func ImportJSON(r io.Reader, e *core.Engine, maxContent int) (*ImportResult, error) {
	result := &ImportResult{}

	// Parse all records first (validate before mutating).
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0), 10*1024*1024) // 10MB line buffer
	var records []ExportRecord
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(records) >= maxImportRecords {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("stopped at %d records (maximum)", maxImportRecords))
			break
		}

		var rec ExportRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			result.Errors++
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("line %d: invalid JSON", lineNum))
			continue
		}

		// Validate content.
		content, _ := rec.Properties["content_full"].(string)
		if content == "" {
			result.Skipped++
			continue
		}
		if !utf8.ValidString(content) {
			result.Errors++
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("line %d: invalid UTF-8", lineNum))
			continue
		}
		if maxContent > 0 && len(content) > maxContent {
			result.Errors++
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("line %d: content exceeds max length", lineNum))
			continue
		}

		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read input: %w", err)
	}

	if len(records) == 0 {
		return result, nil
	}

	// Import under a single write batch. WithWriteBatch handles
	// lock + shared bbolt tx + save, amortizing fsync across both
	// passes: node creation + property indexing first, then edge
	// creation referencing the fresh IDs. For a bulk import of N
	// nodes with ~K edges each, edge-creation cost drops from
	// O(N*K) fsyncs to O(1). (P2-06.)
	idMap := make(map[string]string, len(records))
	batchErr := e.WithWriteBatch("import", func(ws *core.WriteSession) (bool, error) {
		// Pass 1: create nodes, build old-to-new ID map.
		for _, rec := range records {
			props := buildSafeProps(rec.Properties)
			props["processing_status"] = graph.StringProperty("captured")
			props["access_count"] = graph.Int64Property(0)
			if _, ok := props["created_at"]; !ok {
				props["created_at"] = graph.TimestampProperty(time.Now().UTC())
			}
			if rec.ID != "" {
				props["imported_from_id"] = graph.StringProperty(rec.ID)
			}

			n := ws.AddNode(props)
			for k, v := range n.Properties {
				ws.PropIdx().AddTx(ws.Tx(), n.ID, k, v)
			}

			if rec.ID != "" {
				idMap[rec.ID] = n.ID
			}
			result.Imported++
		}

		// Pass 2: create edges between imported records only.
		for _, rec := range records {
			newSourceID, ok := idMap[rec.ID]
			if !ok {
				continue
			}
			for _, edge := range rec.Edges {
				if edge.Direction != "outbound" {
					continue // only create outbound edges to avoid duplicates
				}
				newTargetID, ok := idMap[edge.TargetID]
				if !ok {
					continue // target not in import batch
				}
				if _, err := ws.AddEdge(newSourceID, newTargetID, edge.Type, edge.Weight, nil); err != nil {
					slog.Error("failed to add edge during import",
						"component", "import", "from", newSourceID, "to", newTargetID, "type", edge.Type, "err", err)
				}
			}
		}

		return result.Imported > 0, nil
	})
	if batchErr != nil {
		return result, fmt.Errorf("import batch: %w", batchErr)
	}

	return result, nil
}

// ImportCSV reads a CSV file with a header row and creates records.
// Column mapping is by header name, with common aliases supported.
func ImportCSV(r io.Reader, e *core.Engine, maxContent int) (*ImportResult, error) {
	result := &ImportResult{}

	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return result, fmt.Errorf("read CSV header: %w", err)
	}

	// Map column indices to property names.
	colMap := make(map[int]string)
	for i, col := range header {
		name := strings.TrimSpace(strings.ToLower(col))
		switch name {
		case "summary", "title", "name":
			name = "content_short"
		case "content", "body", "text":
			name = "content_full"
		case "tags":
			name = "content_keywords"
		case "source", "url", "path":
			name = "source_ref"
		case "date", "created", "timestamp":
			name = "created_at"
		case "type":
			name = "knowledge_type"
		}
		if safeProperties[name] {
			colMap[i] = name
		}
	}

	// Accumulate all valid records.
	var allProps []map[string]any
	rowNum := 1
	for {
		if len(allProps) >= maxImportRecords {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("stopped at %d records (maximum)", maxImportRecords))
			break
		}

		rowNum++
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors++
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("row %d: CSV parse error", rowNum))
			continue
		}

		props := make(map[string]any)
		for i, val := range row {
			name, ok := colMap[i]
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			if val == "" {
				continue
			}
			props[name] = val
		}

		content, _ := props["content_full"].(string)
		if content == "" {
			if short, ok := props["content_short"].(string); ok && short != "" {
				props["content_full"] = short
				content = short
			} else {
				result.Skipped++
				continue
			}
		}
		if !utf8.ValidString(content) {
			result.Errors++
			continue
		}
		if maxContent > 0 && len(content) > maxContent {
			result.Errors++
			continue
		}

		allProps = append(allProps, props)
	}

	if len(allProps) == 0 {
		return result, nil
	}

	// Import under write lock.
	e.Lock()
	defer e.Unlock()

	for _, props := range allProps {
		safeP := buildSafeProps(props)
		safeP["processing_status"] = graph.StringProperty("captured")
		safeP["access_count"] = graph.Int64Property(0)
		if _, ok := safeP["created_at"]; !ok {
			safeP["created_at"] = graph.TimestampProperty(time.Now().UTC())
		}

		n := e.Graph().AddNode(safeP)
		for k, v := range n.Properties {
			e.PropIdx().Add(n.ID, k, v)
		}
		result.Imported++
	}

	if result.Imported > 0 {
		if _, err := e.Save("import csv"); err != nil {
			return result, fmt.Errorf("save after csv import: %w", err)
		}
	}

	return result, nil
}

// ImportObsidian walks an Obsidian vault directory and imports .md files.
func ImportObsidian(vaultPath string, e *core.Engine, maxContent int) (*ImportResult, error) {
	result := &ImportResult{}

	// Collect all .md files.
	type mdFile struct {
		path     string
		name     string // filename without extension
		content  string
		tags     []string
		links    []string // [[wikilink]] targets
	}
	var files []mdFile

	err := filepath.Walk(vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			// Skip hidden directories.
			if strings.HasPrefix(filepath.Base(path), ".") && path != vaultPath {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		if len(files) >= maxImportRecords {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("read %s: %s", path, err))
			return nil
		}

		content := string(data)
		if !utf8.ValidString(content) {
			result.Errors++
			return nil
		}
		if maxContent > 0 && len(content) > maxContent {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("skipped %s: exceeds max content length", path))
			return nil
		}

		name := strings.TrimSuffix(filepath.Base(path), ".md")
		rel, _ := filepath.Rel(vaultPath, path)

		// Parse frontmatter.
		body, tags := parseFrontmatter(content)

		// Extract [[wikilinks]].
		links := extractWikilinks(body)

		files = append(files, mdFile{
			path:    rel,
			name:    name,
			content: body,
			tags:    tags,
			links:   links,
		})
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("walk vault: %w", err)
	}

	if len(files) == 0 {
		return result, nil
	}

	// Import under a single write batch (see ImportJSON for
	// rationale; same structure).
	nameToID := make(map[string]string, len(files))
	edgesCreated := 0
	batchErr := e.WithWriteBatch("import obsidian", func(ws *core.WriteSession) (bool, error) {
		// Pass 1: create nodes, build name-to-ID map for wikilinks.
		for _, f := range files {
			props := graph.Properties{
				"content_full":      graph.StringProperty(f.content),
				"content_short":     graph.StringProperty(strutil.TruncateRunes(f.name, 200)),
				"source_ref":        graph.StringProperty(f.path),
				"processing_status": graph.StringProperty("captured"),
				"created_at":        graph.TimestampProperty(time.Now().UTC()),
				"access_count":      graph.Int64Property(0),
			}
			if len(f.tags) > 0 {
				props["content_keywords"] = graph.StringListProperty(f.tags)
			}

			n := ws.AddNode(props)
			for k, v := range n.Properties {
				ws.PropIdx().AddTx(ws.Tx(), n.ID, k, v)
			}

			nameToID[strings.ToLower(f.name)] = n.ID
			result.Imported++
		}

		// Pass 2: create edges from wikilinks.
		for _, f := range files {
			sourceID, ok := nameToID[strings.ToLower(f.name)]
			if !ok {
				continue
			}
			for _, link := range f.links {
				targetID, ok := nameToID[strings.ToLower(link)]
				if !ok {
					continue // unresolved link
				}
				if sourceID == targetID {
					continue
				}
				if _, err := ws.AddEdge(sourceID, targetID, "related_to", 0.5, nil); err != nil {
					slog.Error("failed to add wikilink edge during obsidian import",
						"component", "import", "from", sourceID, "to", targetID, "err", err)
				}
				edgesCreated++
			}
		}

		return result.Imported > 0, nil
	})
	if batchErr != nil {
		return result, fmt.Errorf("import obsidian batch: %w", batchErr)
	}

	if edgesCreated > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("created %d edges from wikilinks", edgesCreated))
	}

	return result, nil
}

// buildSafeProps converts a map[string]any to graph.Properties,
// only including safe properties. Type coercion is driven by
// safePropTypes (single source of truth). Adding a new safe field
// only requires one entry in that map.
func buildSafeProps(raw map[string]any) graph.Properties {
	props := make(graph.Properties)
	for k, v := range raw {
		t, ok := safePropTypes[k]
		if !ok {
			continue
		}
		switch t {
		case propTypeString:
			if s, ok := v.(string); ok {
				props[k] = graph.StringProperty(s)
			}
		case propTypeFloat:
			switch val := v.(type) {
			case float64:
				props[k] = graph.Float64Property(val)
			case string:
				var f float64
				if _, err := fmt.Sscanf(val, "%f", &f); err == nil {
					props[k] = graph.Float64Property(f)
				}
			}
		case propTypeInt64:
			switch val := v.(type) {
			case float64:
				props[k] = graph.Int64Property(int64(val))
			}
		case propTypeStringList:
			switch val := v.(type) {
			case []any:
				var kw []string
				for _, item := range val {
					if s, ok := item.(string); ok {
						kw = append(kw, s)
					}
				}
				if len(kw) > 0 {
					props[k] = graph.StringListProperty(kw)
				}
			case string:
				parts := strings.Split(val, ";")
				var kw []string
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						kw = append(kw, p)
					}
				}
				if len(kw) > 0 {
					props[k] = graph.StringListProperty(kw)
				}
			}
		case propTypeTimestamp:
			if s, ok := v.(string); ok {
				if ts, err := time.Parse(time.RFC3339, s); err == nil {
					props[k] = graph.TimestampProperty(ts)
				}
			}
		}
	}
	return props
}

// parseFrontmatter extracts YAML frontmatter tags from markdown content.
// Returns the body (without frontmatter) and any tags found.
// Only parses simple scalar and string array values -- rejects complex types.
func parseFrontmatter(content string) (string, []string) {
	if !strings.HasPrefix(content, "---\n") {
		return content, nil
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return content, nil
	}

	fm := content[4 : 4+end]
	body := strings.TrimSpace(content[4+end+4:])

	// Simple line-by-line YAML parsing for tags only.
	// Avoids yaml.v3 to prevent type coercion attacks.
	var tags []string
	inTags := false
	for _, line := range strings.Split(fm, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "tags:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "tags:"))
			if val != "" {
				// Inline format: tags: [a, b, c]
				val = strings.Trim(val, "[]")
				for _, t := range strings.Split(val, ",") {
					t = strings.TrimSpace(t)
					t = strings.Trim(t, "\"'")
					if t != "" {
						tags = append(tags, t)
					}
				}
			} else {
				inTags = true
			}
			continue
		}

		if inTags {
			if strings.HasPrefix(trimmed, "- ") {
				tag := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				tag = strings.Trim(tag, "\"'")
				if tag != "" {
					tags = append(tags, tag)
				}
			} else {
				inTags = false
			}
		}
	}

	return body, tags
}

var wikilinkRegex = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)

// extractWikilinks finds all [[Target]] and [[Target|Display]] links.
func extractWikilinks(content string) []string {
	matches := wikilinkRegex.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var links []string
	for _, m := range matches {
		target := strings.TrimSpace(m[1])
		lower := strings.ToLower(target)
		if !seen[lower] {
			seen[lower] = true
			links = append(links, target)
		}
	}
	return links
}

