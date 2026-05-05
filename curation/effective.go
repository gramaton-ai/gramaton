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
// a collection node. Kept in sync with api/collection_config.go's
// Default* constants (api/curation can't share a const without a
// cycle; tests pin equivalence at the inspect surface).
//
// curation defaults to "none": LLM costs are explicitly opt-in via
// templates that declare curation=standard or via explicit caller
// values, not via the default. Templates that want LLM enrichment
// declare curation=standard explicitly. The flip from "standard"
// happened when activation made the knob load-bearing on collection
// items -- standard had been theatre while content_full was
// uniformly absent.
//
// supersession defaults to "collection" (intra-collection scope);
// MemoryOrphan defaults to "store" for back-compat with today's
// global Memory dedup.
//
// contradictions defaults to "on" matching the additive-knob /
// most-permissive-wins resolution principle.
const (
	collectionDefaultCuration       = "none"
	collectionDefaultSupersession   = "collection"
	collectionDefaultContradictions = "on"
)

// EffectiveConfig is the resolved per-record curation behaviour,
// derived from the record's collection memberships and the
// destructive-vs-additive resolution rule. Values are the underlying
// string forms of the three knobs; api callers can cast to
// api.Curation / api.Supersession / api.Contradictions when the
// typed forms are required (e.g. for response serialisation).
//
// JSON tags lowercase the field names for API responses
// (gramaton_inspect surfaces this struct as an effective_curation
// object).
type EffectiveConfig struct {
	Curation       string `json:"curation"`
	Supersession   string `json:"supersession"`
	Contradictions string `json:"contradictions"`
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

// shouldAutoSupersede returns true if the auto-supersession path
// may consolidate a candidate pair (a, b). Encodes the per-pair
// rule that flows from each record's effective supersession value:
//
//   - If either record's effective supersession is "none", skip.
//     A "none" record opted out of auto-supersession entirely.
//   - If both records are at "store" scope, fire. This is the
//     legacy global-dedup path, used by Memory orphan records.
//   - Otherwise (at least one record is at "collection" scope),
//     require the pair to share at least one member_of collection.
//     Cross-collection pairs at "collection" scope correctly skip
//     -- this is the bug fix Phase 5 ships.
//
// Mixed "collection" + "store" with a shared collection: fires.
// The "collection"-scope record's contract ("only supersede with
// records that share my collection") is satisfied; the "store"-
// scope record's broader contract is also satisfied, since a
// shared-collection peer is a subset of "anywhere in the store".
func shouldAutoSupersede(g graph.NodeReader, aID, bID string) bool {
	eA := EffectiveCurationFor(g, aID)
	eB := EffectiveCurationFor(g, bID)
	if eA.Supersession == "none" || eB.Supersession == "none" {
		return false
	}
	if eA.Supersession == "store" && eB.Supersession == "store" {
		return true
	}
	return shareCollection(g, aID, bID)
}

// shareCollection returns true if a and b have at least one
// member_of edge target in common. O(M+N) where M and N are the
// edge counts of each record.
func shareCollection(g graph.NodeReader, aID, bID string) bool {
	aColls := make(map[string]struct{})
	for _, e := range g.EdgesFrom(aID) {
		if e.Type == "member_of" {
			aColls[e.TargetID] = struct{}{}
		}
	}
	if len(aColls) == 0 {
		return false
	}
	for _, e := range g.EdgesFrom(bID) {
		if e.Type == "member_of" {
			if _, ok := aColls[e.TargetID]; ok {
				return true
			}
		}
	}
	return false
}
