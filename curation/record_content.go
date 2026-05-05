package curation

import (
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// propContentFields parallels api/collection_config.go's constant of
// the same name. Duplicated because curation cannot import api (api
// imports curation, the reverse would cycle). End-to-end tests in
// api/ pin the property name by exercising real CollectionCreate
// flows against curation readers.
const propContentFields = "collection_content_fields"

// RecordContentFor returns the LLM/embedding-grade text for a node,
// suited as input to classify, summarize, observation extraction,
// concept synthesis, and contradiction detection.
//
// For Memory records (content_full set), passes through
// content_full. For collection items, walks member_of edges to find
// a collection with content_fields declared and applies them via
// core.RecordContent. Falls back to a wide concat of every field.*
// string when no governing collection declares content_fields
// (schemaless / curation=none path).
//
// Multi-collection: when a record is a member of multiple
// collections, the first one with content_fields declared wins.
// Multi-membership isn't reachable through the current API surface;
// when/if it becomes reachable, this resolver may need to align
// with EffectiveCurationFor's most-permissive principle.
func RecordContentFor(g graph.NodeReader, recordID string) string {
	n, ok := g.GetNode(recordID)
	if !ok {
		return ""
	}
	return core.RecordContent(n, contentFieldsFor(g, recordID))
}

// contentFieldsFor returns the content_fields list for the first
// member_of collection that declares one. Returns nil when the
// record is an orphan or all governing collections lack
// content_fields.
func contentFieldsFor(g graph.NodeReader, recordID string) []string {
	for _, e := range g.EdgesFrom(recordID) {
		if e.Type != "member_of" {
			continue
		}
		coll, ok := g.GetNode(e.TargetID)
		if !ok {
			continue
		}
		if cf, ok := coll.Properties.GetStringList(propContentFields); ok && len(cf) > 0 {
			return cf
		}
	}
	return nil
}
