package backup

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

const maxImportRecords = 10000

// ImportResult summarizes what an import operation did.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   int      `json:"errors"`
	Warnings []string `json:"warnings,omitempty"`
}

// Safe properties that imports can set. Everything else is rejected.
var safeProperties = map[string]bool{
	"content_full":        true,
	"content_short":       true,
	"content_abstract":    true,
	"content_keywords":    true,
	"source_ref":          true,
	"created_at":          true,
	"valid_from":          true,
	"valid_until":         true,
	"context_about":       true,
	"context_who":         true,
	"context_prompted":    true,
	"context_findable_by": true,
	"context_related":     true,
	"temporality":         true,
	"confidence":          true,
	"knowledge_type":      true,
	"epistemic_status":    true,
	"importance":          true,
	"testimony_hops":      true,
	"source_credibility":  true,
	"asserted_as_of":      true,
	"resolution":          true,
	"resolution_note":     true,
}

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

	// Import under write lock.
	e.Lock()
	defer e.Unlock()

	// Pass 1: create nodes, build old-to-new ID map.
	idMap := make(map[string]string, len(records))
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

		n := e.Graph().AddNode(props)
		for k, v := range n.Properties {
			e.PropIdx().Add(n.ID, k, v)
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
			e.Graph().AddEdge(newSourceID, newTargetID, edge.Type, edge.Weight, nil)
		}
	}

	if result.Imported > 0 {
		e.Save("import")
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
		e.Save("import csv")
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

	// Import under write lock.
	e.Lock()
	defer e.Unlock()

	// Pass 1: create nodes, build name-to-ID map for wikilinks.
	nameToID := make(map[string]string, len(files))
	for _, f := range files {
		props := graph.Properties{
			"content_full":      graph.StringProperty(f.content),
			"content_short":     graph.StringProperty(truncate(f.name, 200)),
			"source_ref":        graph.StringProperty(f.path),
			"processing_status": graph.StringProperty("captured"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
			"access_count":      graph.Int64Property(0),
		}
		if len(f.tags) > 0 {
			props["content_keywords"] = graph.StringListProperty(f.tags)
		}

		n := e.Graph().AddNode(props)
		for k, v := range n.Properties {
			e.PropIdx().Add(n.ID, k, v)
		}

		nameToID[strings.ToLower(f.name)] = n.ID
		result.Imported++
	}

	// Pass 2: create edges from wikilinks.
	edgesCreated := 0
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
			e.Graph().AddEdge(sourceID, targetID, "related_to", 0.5, nil)
			edgesCreated++
		}
	}

	if result.Imported > 0 {
		e.Save("import obsidian")
	}

	if edgesCreated > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("created %d edges from wikilinks", edgesCreated))
	}

	return result, nil
}

// buildSafeProps converts a map[string]any to graph.Properties,
// only including safe properties.
func buildSafeProps(raw map[string]any) graph.Properties {
	props := make(graph.Properties)
	for k, v := range raw {
		if !safeProperties[k] {
			continue
		}
		switch k {
		case "content_full", "content_short", "content_abstract",
			"source_ref", "temporality", "knowledge_type",
			"epistemic_status", "context_about", "context_who",
			"context_prompted", "context_findable_by", "context_related",
			"resolution", "resolution_note":
			if s, ok := v.(string); ok {
				props[k] = graph.StringProperty(s)
			}
		case "confidence", "importance", "source_credibility":
			switch val := v.(type) {
			case float64:
				props[k] = graph.Float64Property(val)
			case string:
				// Try parse.
				var f float64
				if _, err := fmt.Sscanf(val, "%f", &f); err == nil {
					props[k] = graph.Float64Property(f)
				}
			}
		case "testimony_hops":
			switch val := v.(type) {
			case float64:
				props[k] = graph.Int64Property(int64(val))
			}
		case "content_keywords":
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
				// Semicolon-separated.
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
		case "created_at", "valid_from", "valid_until", "asserted_as_of":
			if s, ok := v.(string); ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					props[k] = graph.TimestampProperty(t)
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

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}
