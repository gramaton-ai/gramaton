package core

import (
	"sort"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
)

// RecordIndexText returns the lexical-recall text representation of
// a node, for use by BM25 indexing and other "wide" text consumers.
// For Memory records (content_full present), returns content_full
// unchanged. For collection items (no content_full), concatenates
// every field.* string property with single-space separators -- a
// bag-of-words form suited to lexical recall, where users typing
// enum values like "P1" or "open" into search still surface hits.
//
// Iteration order over field.* properties is non-deterministic
// (map iteration). Acceptable for BM25 (bag of words); callers
// needing deterministic order use RecordContent.
func RecordIndexText(n *graph.Node) string {
	if n == nil {
		return ""
	}
	var parts []string
	if s, ok := n.Properties.GetString("content_full"); ok {
		parts = append(parts, s)
	} else if s, ok := n.Properties.GetString("content"); ok {
		// Session segments carry their text under "content"; a
		// rebuild that only reads content_full re-indexes every
		// segment as empty and store="sessions" search goes dark.
		parts = append(parts, s)
	} else if f := collectFieldStrings(n); f != "" {
		parts = append(parts, f)
	}
	// Keywords and meta key:value terms are part of the insert-time
	// BM25 text (save appends meta; update appends both); a rebuild
	// must reproduce the same TERM SET or those search contracts
	// silently degrade after any revert, checkout, restore, or
	// index-loss boot. List-valued meta emits one key:elem term per
	// element to match the insert paths; keys are sorted so the
	// output is deterministic.
	if kws, ok := n.Properties.GetStringList("content_keywords"); ok && len(kws) > 0 {
		parts = append(parts, strings.Join(kws, " "))
	}
	metaKeys := make([]string, 0, 4)
	for key := range n.Properties {
		if strings.HasPrefix(key, "meta.") {
			metaKeys = append(metaKeys, key)
		}
	}
	sort.Strings(metaKeys)
	for _, key := range metaKeys {
		bare := strings.TrimPrefix(key, "meta.")
		v := n.Properties[key]
		if elems := v.StringList(); len(elems) > 0 {
			for _, elem := range elems {
				parts = append(parts, bare+":"+elem)
			}
			continue
		}
		parts = append(parts, bare+":"+v.FormatValue())
	}
	return strings.Join(parts, " ")
}

// RecordContent returns the LLM/embedding-grade text representation
// of a node. For Memory records (content_full present), returns
// content_full unchanged. For collection items in schema'd
// collections, joins the field.<name> values for each name in
// contentFields in the declared order, newline-separated. Falls
// back to RecordIndexText output when contentFields is empty
// (the schemaless ad-hoc path).
//
// Missing or empty fields named in contentFields are skipped --
// not rendered as blank lines. Non-string field types named in
// contentFields would be skipped here, but schema validation
// rejects them at the collection-create boundary so that case
// shouldn't reach this helper.
func RecordContent(n *graph.Node, contentFields []string) string {
	if n == nil {
		return ""
	}
	if s, ok := n.Properties.GetString("content_full"); ok {
		return s
	}
	if len(contentFields) == 0 {
		return collectFieldStrings(n)
	}
	parts := make([]string, 0, len(contentFields))
	for _, name := range contentFields {
		if s, ok := n.Properties.GetString("field." + name); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func collectFieldStrings(n *graph.Node) string {
	var parts []string
	for k := range n.Properties {
		if !strings.HasPrefix(k, "field.") {
			continue
		}
		if s, ok := n.Properties.GetString(k); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// RecordContentFromFields returns the LLM/embedding-grade text for
// a collection item being constructed from a fields map, before
// the item's graph node exists. Output matches RecordContent(n,
// contentFields) for any node built from these fields. Used by
// CollectionAdd / CollectionAddBatch at insert time, where the
// embedding must be computed outside the engine lock and the
// authoritative node is only created later under the lock.
//
// When contentFields is empty, falls back to a wide concatenation
// of every string-typed value (schemaless / no-template path),
// parallel to RecordIndexText for finished nodes.
func RecordContentFromFields(contentFields []string, fields map[string]any) string {
	if len(contentFields) == 0 {
		var parts []string
		for _, v := range fields {
			if s, ok := v.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	parts := make([]string, 0, len(contentFields))
	for _, name := range contentFields {
		if v, ok := fields[name]; ok {
			if s, ok := v.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}
