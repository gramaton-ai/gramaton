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
	propSupersession   = "collection_supersession"
	propContradictions = "collection_contradictions"
	propKnowledgeType  = "knowledge_type"
)

// MemoryOrphan defaults: applied when a record has no member_of
// edges (Memory record, not in any collection).
//
// curation=standard preserves today's full-pipeline behaviour on
// captured Memory records. supersession=store preserves global
// dedup. contradictions=on preserves contradiction detection.
const (
	MemoryOrphanCuration       = "standard"
	MemoryOrphanSupersession   = "store"
	MemoryOrphanContradictions = "on"
)

// Collection-level defaults applied when the property is absent on
// a collection node. Diverge from MemoryOrphan only on supersession
// (collection-default is intra-collection scope; orphan-default is
// store-wide for back-compat with today's Memory behaviour).
const (
	collectionDefaultCuration       = "standard"
	collectionDefaultSupersession   = "collection"
	collectionDefaultContradictions = "on"
)

// EffectiveConfig is the resolved per-record curation behaviour,
// derived from the record's collection memberships and the
// destructive-vs-additive resolution rule. Values are the underlying
// string forms of the three knobs; api callers can cast to
// api.Curation / api.Supersession / api.Contradictions when the
// typed forms are required (e.g. for response serialisation).
type EffectiveConfig struct {
	Curation       string
	Supersession   string
	Contradictions string
}

// EffectiveCurationFor returns the effective curation, supersession,
// and contradictions settings for a record by walking its member_of
// edges and applying per-knob resolution rules.
//
// Records with no member_of edges (memory orphans) get the memory
// store defaults: standard / store / on.
//
// Records with one or more memberships have each collection's
// settings combined per knob:
//
//   - supersession is destructive (sets valid_until on records).
//     Most-restrictive wins. Order: none > collection > store.
//
//   - curation is additive (LLM stages add metadata fields).
//     Most-permissive wins. Order: standard > none.
//
//   - contradictions is additive (creates contradicts edges).
//     Most-permissive wins. Order: on > off.
//
// One-line principle: never irreversibly modify a record without
// unanimous agreement; always enrich when any collection wants it.
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
		Supersession:   MemoryOrphanSupersession,
		Contradictions: MemoryOrphanContradictions,
	}
	if len(collectionIDs) == 0 {
		return orphan
	}

	var (
		curation       string
		supersession   string
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
		sup := readSupersession(n)
		con := readContradictions(n)

		if !seen {
			curation = cur
			supersession = sup
			contradictions = con
			seen = true
			continue
		}
		supersession = mostRestrictiveSupersession(supersession, sup)
		curation = mostPermissiveCuration(curation, cur)
		contradictions = mostPermissiveContradictions(contradictions, con)
	}

	if !seen {
		return orphan
	}
	return EffectiveConfig{
		Curation:       curation,
		Supersession:   supersession,
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

func readSupersession(n *graph.Node) string {
	v, _ := n.Properties.GetString(propSupersession)
	if v == "" {
		return collectionDefaultSupersession
	}
	return v
}

func readContradictions(n *graph.Node) string {
	v, _ := n.Properties.GetString(propContradictions)
	if v == "" {
		return collectionDefaultContradictions
	}
	return v
}

// mostRestrictiveSupersession picks the value highest in the
// "more restrictive" order: none > collection > store.
func mostRestrictiveSupersession(a, b string) string {
	rank := map[string]int{"none": 2, "collection": 1, "store": 0}
	if rank[a] >= rank[b] {
		return a
	}
	return b
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
