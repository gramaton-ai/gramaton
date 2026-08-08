package curation

import (
	"encoding/hex"
	"hash/fnv"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
)

// repairInputHashKey records the hash of the content_short value that
// produced the most recent Tier-4 flag. The cascade short-circuits
// when the property is set, repair_needed_llm is true, and the hash
// matches the current content_short -- avoiding the redundant
// sanitize + 3 SetProp writes per Tier-4 record on every server boot.
const repairInputHashKey = "repair_input_hash"

// SelfHealResult summarizes one self-heal pass over the store.
// Repaired counts records where a stored summary was rewritten;
// FlaggedForLLM records where strip + truncate + content_full
// fallback all produced an unusable result, so the record got a
// `repair_needed_llm` property set for a future LLM-escalation
// pass (not implemented in the initial Phase 3 landing).
type SelfHealResult struct {
	Scanned       int `json:"scanned"`
	Repaired      int `json:"repaired"`
	FlaggedForLLM int `json:"flagged_for_llm"`
}

// selfHealOutcome labels which tier of the repair cascade actually
// handled a record. Used for the per-record log + audit property so
// we can see, post-hoc, which tier ships enough salvage and which
// ones fire rarely.
type selfHealOutcome string

const (
	outcomeClean    selfHealOutcome = "clean"    // no contamination
	outcomeStripped selfHealOutcome = "stripped" // Tier-1 strip preserved enough prose
	outcomeFallback selfHealOutcome = "fallback" // strip yielded empty; content_full first-sentences used
	outcomeFlagged  selfHealOutcome = "flagged"  // nothing salvageable; record flagged for LLM repair
)

// minSummaryAfterStrip is the floor below which a stripped result is
// considered unusable as a summary — the strip landed too early in
// the original string and threw away too much. 50 characters is
// enough for a one-sentence summary; below that the fallback path
// (extract from content_full) produces a better result.
const minSummaryAfterStrip = 50

// sentenceEnder splits prose into sentences. Conservative — only
// treats `.`, `?`, `!` followed by whitespace or end-of-string as a
// terminator. Matches Gramaton's summary style (declarative prose
// ending in periods) and doesn't mis-fire on abbreviations mid-
// sentence because of the whitespace requirement.
var sentenceEnder = regexp.MustCompile(`[.!?](\s|$)`)

// DetectAndRepairSummary runs the Phase 3 self-heal cascade on one
// record. Pure on input: reads content_short + content_full, writes
// new content_short + optional repair_needed_llm flag + repaired_at
// timestamp. Returns the outcome so the caller can tally counters
// and emit per-record logs.
//
// Cascade:
//
//  1. Detect via sanitize.Field — if stripping yields the same
//     string, the record is clean, no-op.
//  2. Strip tier — if the stripped result is ≥ minSummaryAfterStrip
//     characters, store it.
//  3. Fallback tier — extract the first 1-2 sentences from
//     content_full as a degenerate summary (deterministic, no LLM).
//     Stored when it yields non-empty output of reasonable length.
//  4. Flag tier — nothing salvageable. Set `repair_needed_llm=true`
//     on the record for a future LLM-escalation pass.
//
// Caller must hold the engine write lock.
func DetectAndRepairSummary(e *core.Engine, nodeID string, logger *slog.Logger) selfHealOutcome {
	n, ok := e.Graph().GetNode(nodeID)
	if !ok {
		return outcomeClean
	}
	orig, hasSummary := n.Properties.GetString("content_short")
	if !hasSummary || orig == "" {
		return outcomeClean
	}

	// Tier 1: detect contamination. Share the same strip list as
	// write-site sanitization so "what we reject at capture" and
	// "what we repair post-hoc" stay in lockstep.
	cleaned := sanitize.Field(orig)
	if cleaned == orig {
		// Content is currently clean. If a prior cycle Tier-4 flagged
		// this record against then-contaminated content, the flag is
		// now stale -- something (a manual edit, an external repair,
		// supersession) has rewritten content_short to a clean value.
		// Clear it so a future LLM-escalation pass doesn't pick up
		// records that don't actually need repair.
		clearStaleRepairFlag(e, nodeID, n)
		return outcomeClean
	}

	// Short-circuit when this record was already Tier-4 flagged
	// against the same content_short. The deterministic cascade is
	// pure on input -- re-running it would re-write the same three
	// properties every server boot.
	if flagged, _ := n.Properties.GetBool("repair_needed_llm"); flagged {
		if h, _ := n.Properties.GetString(repairInputHashKey); h != "" && h == hashContentShort(orig) {
			return outcomeClean
		}
	}

	// Contamination detected. Apply repair cascade.

	if len(cleaned) >= minSummaryAfterStrip {
		// Tier 2: strip preserved enough prose. Write the cleaned
		// version and mark embedding stale so the next reembed
		// cycle refreshes it against the corrected summary.
		e.SetContentProp(nodeID, "content_short", cleaned)
		e.SetProp(nodeID, "repaired_at", graph.TimestampProperty(time.Now().UTC()))
		e.SetProp(nodeID, "repair_method", graph.StringProperty(string(outcomeStripped)))
		// Successful repair supersedes any prior Tier-4 flag.
		clearStaleRepairFlag(e, nodeID, n)
		invalidateEmbedding(e, nodeID)
		logger.Info("self-heal: summary repaired via strip",
			"component", "curation", "record", nodeID, "len_before", len(orig), "len_after", len(cleaned))
		return outcomeStripped
	}

	// Tier 3: strip yielded too little. Try first-sentences of the
	// record's full text. RecordContentFor returns content_full for
	// Memory records and the content_fields-driven text for
	// collection items.
	contentFull := RecordContentFor(e.Graph(), nodeID)
	if contentFull != "" {
		fallback := firstSentences(contentFull, 2, 500)
		if fallback != "" && len(fallback) >= minSummaryAfterStrip {
			e.SetContentProp(nodeID, "content_short", fallback)
			e.SetProp(nodeID, "repaired_at", graph.TimestampProperty(time.Now().UTC()))
			e.SetProp(nodeID, "repair_method", graph.StringProperty(string(outcomeFallback)))
			// Successful repair supersedes any prior Tier-4 flag.
			clearStaleRepairFlag(e, nodeID, n)
			invalidateEmbedding(e, nodeID)
			logger.Info("self-heal: summary repaired via content_full fallback",
				"component", "curation", "record", nodeID, "len_before", len(orig), "len_after", len(fallback))
			return outcomeFallback
		}
	}

	// Tier 4: flag for LLM escalation. Don't overwrite the
	// contaminated content_short — there's no salvage and clobbering
	// with empty would lose any residual signal. A future pass that
	// can call the summarizer LLM will pick these up by filtering on
	// repair_needed_llm=true.
	e.SetProp(nodeID, "repair_needed_llm", graph.BoolProperty(true))
	e.SetProp(nodeID, "repaired_at", graph.TimestampProperty(time.Now().UTC()))
	e.SetProp(nodeID, "repair_method", graph.StringProperty(string(outcomeFlagged)))
	e.SetProp(nodeID, repairInputHashKey, graph.StringProperty(hashContentShort(orig)))
	logger.Warn("self-heal: summary unsalvageable by deterministic tiers, flagged for LLM",
		"component", "curation", "record", nodeID, "len_before", len(orig))
	return outcomeFlagged
}

// RunSelfHeal walks all Memory + Session segment records and applies
// DetectAndRepairSummary. Collects totals into SelfHealResult. Safe
// to call from the curation cycle (acquires its own locks) OR from
// a CLI entry point.
func RunSelfHeal(e *core.Engine, logger *slog.Logger) *SelfHealResult {
	logger = ensureLogger(logger)
	result := &SelfHealResult{}

	// Read phase: gather candidate IDs under RLock. Walk the full
	// graph and keep nodes that have a non-empty content_short. The
	// property index's Lookup is value-exact (won't give us "any
	// node with this key set"), so a full walk is the simplest
	// correct path. Cost is linear in node count; acceptable because
	// self-heal runs infrequently (~1 per curation cycle, or ad-hoc
	// via `gramaton repair --content-quality`) and the repair work
	// dominates the walk cost anyway.
	e.RLock()
	var ids []string
	for _, id := range e.Graph().AllNodeIDs() {
		n, ok := e.Graph().GetNode(id)
		if !ok {
			continue
		}
		if s, ok := n.Properties.GetString("content_short"); ok && s != "" {
			ids = append(ids, id)
		}
	}
	e.RUnlock()

	if len(ids) == 0 {
		return result
	}

	// Mutation phase: one lock per record so a large scan doesn't
	// block search for minutes on a store with thousands of
	// contaminated records. Repair decisions are independent per
	// record — no cross-record invariants to protect.
	//
	// Each repair commits inside the lock window that made it.
	// Engine.Save commits whatever the graph currently holds, so a
	// repair left uncommitted when the lock is released belongs to
	// whichever writer saves next: an unrelated user save would carry
	// self-heal's mutations under its own author and message. Saving
	// in-window is also what makes the pass resumable — a crash
	// mid-scan keeps the repairs already made.
	for _, id := range ids {
		result.Scanned++
		e.Lock()
		outcome := DetectAndRepairSummary(e, id, logger)
		var repaired bool
		switch outcome {
		case outcomeStripped, outcomeFallback:
			result.Repaired++
			repaired = true
		case outcomeFlagged:
			result.FlaggedForLLM++
			repaired = true
		}
		var saveErr error
		if repaired {
			_, saveErr = e.Save("curation: self-heal summary repairs", graph.CommitAction{
				Kind: graph.ActionCurationSelfHeal, RecordID: id,
			})
		}
		e.Unlock()
		if saveErr != nil {
			logger.Warn("self-heal: save failed",
				"component", "curation", "record", id, "err", saveErr)
		}
	}

	logger.Info("self-heal complete",
		"component", "curation",
		"scanned", result.Scanned,
		"repaired", result.Repaired,
		"flagged_for_llm", result.FlaggedForLLM)

	return result
}

// firstSentences extracts the first maxSentences sentences from s,
// capped at maxChars total. Returns "" when s has no sentence-ending
// punctuation at all (most likely a fragment, not safe to use as a
// summary).
func firstSentences(s string, maxSentences, maxChars int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	matches := sentenceEnder.FindAllStringIndex(s, maxSentences)
	if len(matches) == 0 {
		return ""
	}
	end := matches[len(matches)-1][0] + 1 // include the punctuation
	if end > len(s) {
		end = len(s)
	}
	out := strings.TrimSpace(s[:end])
	if len(out) > maxChars {
		// Rune-safe truncate at the last sentence boundary within
		// maxChars, so we don't mid-byte-slice a UTF-8 character.
		// Find the last sentence-end that fits; if none, hard-cut.
		sub := out[:maxChars]
		lastMatches := sentenceEnder.FindAllStringIndex(sub, -1)
		if len(lastMatches) > 0 {
			end := lastMatches[len(lastMatches)-1][0] + 1
			out = strings.TrimSpace(sub[:end])
		} else {
			runes := []rune(out)
			if len(runes) > maxChars {
				runes = runes[:maxChars]
			}
			out = string(runes)
		}
	}
	return out
}

// hashContentShort returns a 16-char hex-FNV64 of s. Used to detect
// whether content_short has changed since a prior Tier-4 flag so the
// cascade can skip redundant work on every boot. Collision risk is
// acceptable: a collision wastes one cycle of cascade work, identical
// to the pre-fix behavior.
func hashContentShort(s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// clearStaleRepairFlag clears a prior Tier-4 flag (repair_needed_llm
// + repair_input_hash) when the flag is currently set on the node.
// Called from every cascade outcome path that represents a successful
// repair (or a now-clean state) -- Tier-1 clean, Tier-2 stripped,
// Tier-3 fallback. Tier-4 itself never calls this; the flag is the
// outcome.
//
// Conditioned on the flag being set so we don't churn the bbolt
// index writing zero values onto records that never had the flag.
func clearStaleRepairFlag(e *core.Engine, nodeID string, n *graph.Node) {
	flagged, _ := n.Properties.GetBool("repair_needed_llm")
	if !flagged {
		return
	}
	e.SetProp(nodeID, "repair_needed_llm", graph.BoolProperty(false))
	if _, hasHash := n.Properties.GetString(repairInputHashKey); hasHash {
		e.SetProp(nodeID, repairInputHashKey, graph.StringProperty(""))
	}
}

// invalidateEmbedding marks a record's embedding as stale so the
// next reembed cycle refreshes it against the repaired content.
// We do this by clearing embedding_model -- the reembed pass uses
// a missing-or-different model string as the "needs reembed" signal.
// The actual embedding vector is left in place (still usable until
// reembed runs) to avoid a temporary quality hole in search.
func invalidateEmbedding(e *core.Engine, nodeID string) {
	e.SetProp(nodeID, "embedding_model", graph.StringProperty(""))
}
