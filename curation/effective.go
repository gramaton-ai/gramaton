package curation

import (
	"github.com/gramaton-ai/gramaton/graph"
)

// Property names on the collection node, kept in sync with
// api/collection_config.go. Duplicated here because curation cannot
// import api -- api imports curation, and the inverse would be a
// cycle. End-to-end tests in api/ pin equivalence by exercising the
// resolution rule against api-created collections.
const (
	propCuration       = "collection_curation"
	propContradictions = "collection_contradictions"
	propKnowledgeType  = "knowledge_type"
)

// MemoryOrphan defaults: applied when a record has no member_of
// edges (Memory record, not in any collection).
//
// curation=standard preserves the full-pipeline behaviour on
// captured Memory records. contradictions=on preserves contradiction
// detection.
const (
	MemoryOrphanCuration       = "standard"
	MemoryOrphanContradictions = "on"
)

// Collection-level defaults applied when the property is absent on
// a collection node. Kept in sync with api/collection_config.go's
// Default* constants (api/curation can't share a const without a
// cycle; tests pin equivalence at the inspect surface).
//
// curation defaults to "none": LLM costs are explicitly opt-in via
// templates that declare curation=standard or via explicit caller
// values, not via the default. Templates that want LLM enrichment
// declare curation=standard explicitly. The earlier "standard"
// default was inert while collection items uniformly lacked
// content_full; it flipped to "none" once the knob began driving
// real spend on every collection.
//
// contradictions defaults to "on" matching the additive-knob /
// most-permissive-wins resolution principle.
const (
	collectionDefaultCuration       = "none"
	collectionDefaultContradictions = "on"
)

// EffectiveConfig is the resolved per-record curation behaviour,
// derived from the record's collection memberships. Both remaining
// knobs are additive (LLM enrichment; contradicts edges), so
// resolution is uniformly most-permissive-wins. The destructive
// supersession knob was removed with auto-supersession: no curation
// pass irreversibly modifies a record any more, so there is nothing
// left for a restrictive knob to protect.
//
// JSON tags lowercase the field names for API responses
// (gramaton_inspect surfaces this struct as an effective_curation
// object).
type EffectiveConfig struct {
	Curation       string `json:"curation"`
	Contradictions string `json:"contradictions"`
}

// EffectiveCurationFor returns the effective curation and
// contradictions settings for a record by walking its member_of
// edges and applying per-knob resolution rules.
//
// Records with no member_of edges (memory orphans) get the memory
// store defaults: standard / on.
//
// Records with one or more memberships have each collection's
// settings combined per knob, most-permissive wins:
//
//   - curation (LLM stages add metadata fields): standard > none.
//   - contradictions (creates contradicts edges): on > off.
//
// One-line principle: always enrich when any collection wants it.
//
// The function is read-only and acquires no locks; callers hold
// either RLock or Lock for the duration.
func EffectiveCurationFor(g graph.NodeReader, recordID string) EffectiveConfig {
	var collectionIDs []string
	for _, e := range g.EdgesFrom(recordID) {
		if e.Type == "member_of" {
			collectionIDs = append(collectionIDs, e.TargetID)
		}
	}
	orphan := EffectiveConfig{
		Curation:       MemoryOrphanCuration,
		Contradictions: MemoryOrphanContradictions,
	}
	if len(collectionIDs) == 0 {
		return orphan
	}

	var (
		curation       string
		contradictions string
		seen           bool
	)
	for _, cID := range collectionIDs {
		n, ok := g.GetNode(cID)
		if !ok {
			continue // stale member_of edge to a deleted node
		}
		if knType, _ := n.Properties.GetString(propKnowledgeType); knType != "collection" {
			continue // edge points to a non-collection node
		}

		cur := readCuration(n)
		con := readContradictions(n)

		if !seen {
			curation = cur
			contradictions = con
			seen = true
			continue
		}
		curation = mostPermissiveCuration(curation, cur)
		contradictions = mostPermissiveContradictions(contradictions, con)
	}

	if !seen {
		return orphan
	}
	return EffectiveConfig{
		Curation:       curation,
		Contradictions: contradictions,
	}
}

// readCuration reads the collection's curation property, normalising
// legacy 4-level enum values: "minimal" -> "none", "full" ->
// "standard". Empty -> the collection-level default ("standard").
// Mirrors api.CollectionCuration so collections written by either
// path produce the same effective value.
func readCuration(n *graph.Node) string {
	v, _ := n.Properties.GetString(propCuration)
	switch v {
	case "":
		return collectionDefaultCuration
	case "minimal":
		return "none"
	case "full":
		return "standard"
	default:
		return v
	}
}

func readContradictions(n *graph.Node) string {
	v, _ := n.Properties.GetString(propContradictions)
	if v == "" {
		return collectionDefaultContradictions
	}
	return v
}

// mostPermissiveCuration picks "standard" if either input wants
// LLM analysis on. Order: standard > none.
func mostPermissiveCuration(a, b string) string {
	if a == "standard" || b == "standard" {
		return "standard"
	}
	return "none"
}

// mostPermissiveContradictions picks "on" if either input wants
// contradiction detection on.
func mostPermissiveContradictions(a, b string) string {
	if a == "on" || b == "on" {
		return "on"
	}
	return "off"
}
