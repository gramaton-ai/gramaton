package api

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// Collection-level behaviour knobs. Three orthogonal axes that
// together determine how curation treats records in a collection:
//
//   - clear_mode     -- what `clear` does to items (resolve vs unlink).
//   - curation       -- LLM-analysis intensity (none/standard).
//   - contradictions -- whether contradicts edges are generated (on/off).
//
// These live as node properties on the collection (not inside the
// item-schema JSON) so they can be updated without rewriting the
// schema blob, and so consumers that read them don't have to parse
// the schema first.
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

// Curation controls per-collection LLM-analysis intensity. Each
// pipeline stage that consults this knob (classify, summarize,
// observation_extract, concept synthesis) runs only when the
// effective value resolves to standard.
//
//	standard (default) -- LLM analysis runs.
//	none               -- no LLM analysis runs. Records still get
//	                      embedded for vector search; supersession
//	                      and contradictions are governed by their
//	                      own knobs.
//
// Legacy values "minimal" and "full" are normalized on read --
// minimal -> none, full -> standard -- so existing collections keep
// working without a migration sweep. Writes reject the legacy values.
type Curation string

const (
	CurationStandard Curation = "standard"
	CurationNone     Curation = "none"
)

var validCurations = map[Curation]bool{
	CurationStandard: true,
	CurationNone:     true,
}

// Contradictions controls whether the system generates contradicts
// edges between records in this collection and other records. The
// stage is LLM-driven, so off saves cost on collections where
// pairwise contradiction detection is meaningless (journal entries,
// bookmarks, recipe collections).
//
//	on (default) -- contradictions stage runs against records here.
//	off          -- contradictions stage skipped.
type Contradictions string

const (
	ContradictionsOn  Contradictions = "on"
	ContradictionsOff Contradictions = "off"
)

var validContradictions = map[Contradictions]bool{
	ContradictionsOn:  true,
	ContradictionsOff: true,
}

// Default values used when a collection's property is absent. The
// read-time fallback these constants power is why we don't need a
// migrate-time sweep on every config change.
//
// DefaultCuration is CurationNone. Bare-bones collections created
// without a template or explicit curation knob skip LLM work --
// the safe default for "I want a collection, didn't say anything
// else." Templates that want LLM-driven enrichment (backlog, todo,
// reading-list, journal, references) declare curation=standard
// explicitly. The earlier CurationStandard default was inert while
// collection items uniformly lacked content_full and no LLM stage
// actually ran; it flipped to none once the knob began driving real
// spend on every collection -- LLM costs are explicitly opt-in, not
// inherited from a default.
const (
	DefaultClearMode      = ClearModeResolve
	DefaultCuration       = CurationNone
	DefaultContradictions = ContradictionsOn
)

// Property names on the collection node.
//
// propContentFields is a parallel-encoded copy of the schema's
// content_fields list. Stored as a top-level StringList property
// (alongside the JSON-encoded collection_schema) so curation/ --
// which cannot import api/ without a cycle -- can read it directly
// without parsing JSON each cycle.
const (
	propClearMode      = "collection_clear_mode"
	propCuration       = "collection_curation"
	propContradictions = "collection_contradictions"
	propContentFields  = "collection_content_fields"
)

// validateCollectionConfig rejects unknown values for each config
// field. Empty strings are accepted -- they signal "use the default"
// and the getters pick that up. Legacy curation values "minimal" and
// "full" are rejected on writes; reads still accept them and
// normalize.
func validateCollectionConfig(clearMode, curation, contradictions string) error {
	if clearMode != "" && !validClearModes[ClearMode(clearMode)] {
		return fmt.Errorf("clear_mode %q not in {resolve, unlink}", clearMode)
	}
	if curation != "" && !validCurations[Curation(curation)] {
		return fmt.Errorf("curation %q not in {standard, none}", curation)
	}
	if contradictions != "" && !validContradictions[Contradictions(contradictions)] {
		return fmt.Errorf("contradictions %q not in {on, off}", contradictions)
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

// CollectionCuration returns the collection's curation profile,
// falling back to DefaultCuration when absent. Legacy values from
// the pre-redesign 4-level enum are normalized on read so existing
// stores keep working without a migration sweep.
func CollectionCuration(n *graph.Node) Curation {
	if n == nil {
		return DefaultCuration
	}
	v, _ := n.Properties.GetString(propCuration)
	switch v {
	case "":
		return DefaultCuration
	case "minimal":
		return CurationNone
	case "full":
		return CurationStandard
	default:
		return Curation(v)
	}
}

// CollectionContradictions returns the collection's contradictions
// config, falling back to DefaultContradictions when absent.
func CollectionContradictions(n *graph.Node) Contradictions {
	if n == nil {
		return DefaultContradictions
	}
	v, _ := n.Properties.GetString(propContradictions)
	if v == "" {
		return DefaultContradictions
	}
	return Contradictions(v)
}

// initialProcessingStatus picks the processing_status to stamp on a
// new collection-item record based on its collection's curation
// knob. curation=standard items are eligible for the autonomous
// pipeline (captured); curation=none items bypass it (processed).
//
// This is what makes the curation knob mean something at the
// per-record level: the autonomous pipeline filters on
// processing_status=captured, so the knob's value at write time
// determines whether a given item ever sees an LLM stage.
func initialProcessingStatus(c Curation) string {
	if c == CurationNone {
		return "processed"
	}
	return "captured"
}
