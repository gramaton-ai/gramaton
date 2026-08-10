package graph

import (
	"sort"
	"strings"
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
//
// This is the node-local half of the recipe; LexicalDocument wraps
// it with the graph-derived collection context. Callers indexing a
// node that can carry collection context should prefer
// LexicalDocument.
func RecordIndexText(n *Node) string {
	if n == nil {
		return ""
	}
	var parts []string
	base := ""
	if s, ok := n.Properties.GetString("content_full"); ok {
		base = s
	} else if s, ok := n.Properties.GetString("content"); ok {
		// Session segments carry their text under "content"; a
		// rebuild that only reads content_full re-indexes every
		// segment as empty and store="sessions" search goes dark.
		base = s
	} else if f := collectFieldStrings(n); f != "" {
		base = f
	}
	if base != "" {
		parts = append(parts, base)
	}
	// The author-written summary carries prospective search vocabulary
	// (terms a future query would use that the prose itself may not).
	// Insert-time indexing appends the same text via LexicalSummaryText;
	// the two must stay in lockstep or the term set silently diverges
	// after a rebuild.
	if short, ok := n.Properties.GetString("content_short"); ok {
		if s := LexicalSummaryText(base, short); s != "" {
			parts = append(parts, s)
		}
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
		// The typed accessors, not a bare StringList() call: meta
		// values are stored as string, number, bool, or string list,
		// and the Property type asserts on wrong-type access. An
		// empty stored list emits no terms, matching the insert-time
		// builders.
		if elems, ok := n.Properties.GetStringList(key); ok {
			for _, elem := range elems {
				parts = append(parts, bare+":"+elem)
			}
			continue
		}
		parts = append(parts, bare+":"+n.Properties[key].FormatValue())
	}
	return strings.Join(parts, " ")
}

// LexicalSummaryText returns the content_short text to include in a
// record's lexical (BM25) document alongside its base content, or ""
// when the summary adds no vocabulary: it is empty, or it appears
// verbatim inside the base text (observation summaries and chunk
// children store truncated prefixes of their content; re-appending
// those would only distort term frequencies without adding a single
// findable term). A summary that merely paraphrases the content is
// NOT filtered -- its shared terms count twice, which is the
// intended mild boost for summary-worthy vocabulary.
//
// Every BM25 insert path and the rebuild union (RecordIndexText) must
// route summary text through this one guard so insert-time and
// rebuild-time documents carry identical term sets.
func LexicalSummaryText(base, short string) string {
	if short == "" || strings.Contains(base, short) {
		return ""
	}
	return short
}

// LexicalDocument returns the complete BM25 document for a node: the
// RecordIndexText union plus collection context. A collection
// container node (knowledge_type "collection") contributes its name
// and description; a collection item (member_of edge) is prefixed
// with its owning container's name and description, so items in
// collection "Gramaton development" surface for BM25 queries on
// "Gramaton" even when their own fields don't carry the word. The
// insert paths (CollectionAdd, CollectionUpdateItem) build the same
// prefix from the container they already hold; this function is the
// rebuild- and refresh-side twin, so the two must stay token-equal.
//
// g supplies the edge and container lookups; a nil g degrades to
// RecordIndexText alone.
func LexicalDocument(g NodeReader, n *Node) string {
	if n == nil {
		return ""
	}
	base := RecordIndexText(n)
	if kt, ok := n.Properties.GetString("knowledge_type"); ok && kt == "collection" {
		parts := collectionContextTerms(n)
		if base != "" {
			parts = append(parts, base)
		}
		return strings.Join(parts, " ")
	}
	if g == nil {
		return base
	}
	var ctx []string
	for _, e := range g.EdgesFrom(n.ID) {
		if e.Type != "member_of" {
			continue
		}
		if c, ok := g.GetNode(e.TargetID); ok {
			ctx = append(ctx, collectionContextTerms(c)...)
		}
	}
	if len(ctx) == 0 {
		return base
	}
	if base != "" {
		ctx = append(ctx, base)
	}
	return strings.Join(ctx, " ")
}

// collectionContextTerms returns a container node's name and
// description parts, in that order, skipping empties.
func collectionContextTerms(coll *Node) []string {
	var parts []string
	if name, _ := coll.Properties.GetString("collection_name"); name != "" {
		parts = append(parts, name)
	}
	if desc, _ := coll.Properties.GetString("collection_description"); desc != "" {
		parts = append(parts, desc)
	}
	return parts
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
func RecordContent(n *Node, contentFields []string) string {
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

func collectFieldStrings(n *Node) string {
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
