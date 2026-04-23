package api

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// Collection-level behaviour knobs controlling lifecycle ergonomics.
// These live as node properties on the collection (not inside the
// item-schema JSON) so they can be updated without rewriting the
// schema blob, and so consumers that read them (Phase 5 dedup,
// Phase 8 log filters) don't have to parse the schema first.
//
// Defaults match the values the `gramaton migrate` / collection
// creation paths would write explicitly if they ran a sweep; the
// read-time fallback lets us skip the sweep and still give every
// consumer a well-defined answer.

// ClearMode controls what happens when a collection is cleared
// (e.g. via a future `gramaton collection clear` or the resolve
// path on bulk operations).
//
//	resolve (default) -- item records get resolution="completed"
//	                     and valid_until set to the clear time.
//	                     Historical purchase queries keep working.
//	unlink            -- remove the member_of edge at HEAD; the
//	                     item record persists and can be re-linked.
type ClearMode string

const (
	ClearModeResolve ClearMode = "resolve"
	ClearModeUnlink  ClearMode = "unlink"
)

var validClearModes = map[ClearMode]bool{
	ClearModeResolve: true,
	ClearModeUnlink:  true,
}

// Supersession controls the candidate scope for auto-supersession on
// short-content records (Layer 1 of the dedup fix; consumed by
// Phase 5).
//
//	collection (default) -- only records sharing this collection
//	                        participate in same-collection supersession.
//	                        Cross-collection collisions ("eggs" in
//	                        Grocery vs "eggs" in Recipes) don't fire.
//	store                -- legacy behaviour: any record in the store
//	                        is a candidate. Opt-in for knowledge-shaped
//	                        collections where global dedup is wanted.
//	none                 -- collection's items never participate in
//	                        auto-supersession.
type Supersession string

const (
	SupersessionCollection Supersession = "collection"
	SupersessionStore      Supersession = "store"
	SupersessionNone       Supersession = "none"
)

var validSupersessions = map[Supersession]bool{
	SupersessionCollection: true,
	SupersessionStore:      true,
	SupersessionNone:       true,
}

// Curation is the per-collection curation profile. Controls which
// pipeline stages (classify, summary, embed, contradictions,
// concepts) run on items in this collection. Consumed by Phase 5's
// per-stage skip logic.
//
//	full     -- every stage runs.
//	standard (default) -- every stage except summary-for-short-content.
//	minimal  -- only embed + concepts. Right for short-content items
//	            where LLM-expensive stages add no value.
//	none     -- even embed skipped; BM25 search only.
type Curation string

const (
	CurationFull     Curation = "full"
	CurationStandard Curation = "standard"
	CurationMinimal  Curation = "minimal"
	CurationNone     Curation = "none"
)

var validCurations = map[Curation]bool{
	CurationFull:     true,
	CurationStandard: true,
	CurationMinimal:  true,
	CurationNone:     true,
}

// Default values used when a collection's property is absent. The
// read-time fallback these constants power is why Phase 4 doesn't
// need a migrate-time sweep.
const (
	DefaultClearMode    = ClearModeResolve
	DefaultSupersession = SupersessionCollection
	DefaultCuration     = CurationStandard
)

// Property names on the collection node.
const (
	propClearMode    = "collection_clear_mode"
	propSupersession = "collection_supersession"
	propCuration     = "collection_curation"
)

// validateCollectionConfig rejects unknown values for each config
// field. Empty strings are accepted -- they signal "use the default"
// and the getters pick that up.
func validateCollectionConfig(clearMode, supersession, curation string) error {
	if clearMode != "" && !validClearModes[ClearMode(clearMode)] {
		return fmt.Errorf("clear_mode %q not in {resolve, unlink}", clearMode)
	}
	if supersession != "" && !validSupersessions[Supersession(supersession)] {
		return fmt.Errorf("supersession %q not in {collection, store, none}", supersession)
	}
	if curation != "" && !validCurations[Curation(curation)] {
		return fmt.Errorf("curation %q not in {full, standard, minimal, none}", curation)
	}
	return nil
}

// CollectionClearMode returns the collection's clear_mode config,
// falling back to DefaultClearMode when the property is absent or
// holds an empty string.
func CollectionClearMode(n *graph.Node) ClearMode {
	if n == nil {
		return DefaultClearMode
	}
	v, _ := n.Properties.GetString(propClearMode)
	if v == "" {
		return DefaultClearMode
	}
	return ClearMode(v)
}

// CollectionSupersession returns the collection's supersession
// config, falling back to DefaultSupersession when absent.
func CollectionSupersession(n *graph.Node) Supersession {
	if n == nil {
		return DefaultSupersession
	}
	v, _ := n.Properties.GetString(propSupersession)
	if v == "" {
		return DefaultSupersession
	}
	return Supersession(v)
}

// CollectionCuration returns the collection's curation profile,
// falling back to DefaultCuration when absent.
func CollectionCuration(n *graph.Node) Curation {
	if n == nil {
		return DefaultCuration
	}
	v, _ := n.Properties.GetString(propCuration)
	if v == "" {
		return DefaultCuration
	}
	return Curation(v)
}
