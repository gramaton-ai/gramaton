// Package sanitize strips LLM tool-use-format leakage from short
// string fields (summary_short, context_about, etc.) and validates
// the sanitized shape. Lives in internal/ because both api/
// (write-site integration) and curation/ (classification output
// parser) consume it; api/ already imports curation/ which
// forbids the reverse direction.
package sanitize

import (
	"fmt"
	"strings"
)

// badTailTokens are substrings whose presence in a summary / metadata
// field indicates LLM tool-use-format leakage past the intended close
// of the string argument. Observed empirically 2026-04-24: Claude
// (as an MCP-invoking agent) sometimes emits tool-use render
// fragments inside the value of a length-constrained string arg —
// for example, a summary_short that ends in
//
//	"...any future GUI.</summary_short>\n<parameter name=\"keywords\">[..."
//
// The `</summary_short>` tag plus a dangling `<parameter name=`
// attribute are Anthropic tool-use internals that leaked through the
// client's argument parser into the stored JSON string value.
//
// The list is intentionally specific: only tokens that don't appear
// in legitimate summary content. An aggressive strip on all `<`
// characters would mangle records that legitimately discuss code,
// XML, or HTML component names ("React's <Button> component").
//
// If a new pattern is discovered, add it here; the sanitizer strips
// from the first match forward.
var badTailTokens = []string{
	// Claude tool-use XML close tags — most commonly observed.
	"</summary_short>",
	"</content>",
	"</content_full>",
	"</content_short>",
	"</keywords>",
	"</context_about>",
	"</context_who>",
	"</context_findable_by>",
	"</parameter>",
	// Claude tool-use open tag for the *next* parameter, leaking past
	// the current argument's close.
	"<parameter name=",
	// Model stop tokens. Not observed on Anthropic but common on
	// other providers; cheap to include defensively.
	"<|im_end|>",
	"<|im_start|>",
	"<|endoftext|>",
	"<|end_of_text|>",
}

// stripBadTail returns s truncated at the first occurrence of any
// badTailTokens entry. If no token matches, s is returned unchanged.
// The strip is "take everything before the bad token" — anything
// after the token is assumed to be LLM output drift and discarded.
func stripBadTail(s string) string {
	earliest := -1
	for _, tok := range badTailTokens {
		if idx := strings.Index(s, tok); idx >= 0 {
			if earliest == -1 || idx < earliest {
				earliest = idx
			}
		}
	}
	if earliest < 0 {
		return s
	}
	return s[:earliest]
}

// Field cleans an LLM-generated short metadata field
// (summary_short, context_about, context_who, context_findable_by).
// Strips known tool-use-format tail fragments (see badTailTokens),
// then trims surrounding whitespace. The result is safe to store,
// embed, or index.
//
// Does NOT apply to content_full or other freeform content fields —
// those may legitimately contain XML-ish fragments. Use only where
// the field is a short, LLM-generated summary/metadata string.
func Field(s string) string {
	if s == "" {
		return s
	}
	s = stripBadTail(s)
	return strings.TrimSpace(s)
}

// Validate reports an error when the field is invalid after
// sanitization: empty when the original was non-empty (everything
// got stripped — contamination-only input), or exceeds the maximum
// length. Callers typically sanitize first, then validate; the
// pair catches both "blank input" and "input that looked fine but
// was pure garbage" cases with a single shape check.
//
// maxLen is caller-supplied so this helper works for both
// summary_short (typically ~750 chars, hard-capped) and context
// fields (shorter, via MaxContextFieldLen).
func Validate(original, sanitized string, fieldName string, maxLen int) error {
	if strings.TrimSpace(original) == "" {
		// Whitespace-only input is an empty field, not contamination;
		// the caller stores the cleaned empty value.
		return nil
	}
	if sanitized == "" {
		// Input had bytes but sanitization left nothing — pure
		// tool-use-format contamination. Reject; caller should not
		// see this "cleaned" to an empty field because the loss is
		// load-bearing (likely the whole summary was garbage).
		return fmt.Errorf("%s contains only structured-output tokens; rewrite without <parameter>/</tag> fragments", fieldName)
	}
	if len(sanitized) > maxLen {
		return fmt.Errorf("%s exceeds maximum length of %d", fieldName, maxLen)
	}
	return nil
}
