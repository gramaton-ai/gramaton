package graph

import (
	"regexp"
	"strings"
)

// ChunkText splits text into overlapping chunks based on a token budget.
// Uses a simple approximation of 1 token per 4 characters.
// Returns nil if the text is at or below the threshold.
func ChunkText(text string, thresholdTokens, chunkTokens, overlapTokens int) []string {
	charsPerToken := 4
	thresholdChars := thresholdTokens * charsPerToken
	chunkChars := chunkTokens * charsPerToken
	overlapChars := overlapTokens * charsPerToken

	if len(text) <= thresholdChars {
		return nil
	}

	var chunks []string
	start := 0
	for start < len(text) {
		end := start + chunkChars
		if end > len(text) {
			end = len(text)
		}

		// Try to break at a word boundary.
		if end < len(text) {
			boundary := end
			minBound := end - chunkChars/5
			if minBound < start {
				minBound = start
			}
			for boundary > minBound {
				if text[boundary] == ' ' || text[boundary] == '\n' {
					end = boundary
					break
				}
				boundary--
			}
		}

		chunks = append(chunks, text[start:end])

		// Advance based on actual chunk end, minus overlap.
		nextStart := end - overlapChars
		if nextStart <= start {
			nextStart = start + chunkChars/2
		}
		start = nextStart

		// If the remaining text is less than half a chunk, include it
		// in the last chunk rather than creating a tiny fragment.
		if start < len(text) && len(text)-start < chunkChars/2 {
			chunks = append(chunks, text[start:])
			break
		}
	}

	return chunks
}

// Section is a semantically coherent portion of a document, split at
// structural boundaries (headings, numbered sections, paragraphs).
type Section struct {
	Heading string // section heading (empty if split by paragraphs)
	Text    string // full section content including heading line
}

// Heading detection patterns, tried in priority order.
var (
	markdownHeadingRe = regexp.MustCompile(`(?m)^#{1,3} .+`)
	numberedRe        = regexp.MustCompile(`(?m)^\d+\.(\d+\.?)* .+`)
	htmlHeadingRe     = regexp.MustCompile(`(?i)<h[1-6][^>]*>`)
)

// SplitSections splits text into semantically coherent sections based
// on structural cues. Detection priority: markdown headings, numbered
// sections, HTML headings, paragraph breaks. Sections are merged or
// sub-split to stay within [minChars, maxChars].
//
// Returns nil if text is shorter than minChars (no splitting needed).
func SplitSections(text string, minChars, maxChars int) []Section {
	if len(text) <= minChars {
		return nil
	}

	// Try structural detection in priority order.
	var sections []Section
	if locs := markdownHeadingRe.FindAllStringIndex(text, -1); len(locs) > 1 {
		sections = splitAtLocations(text, locs)
	} else if locs := numberedRe.FindAllStringIndex(text, -1); len(locs) > 1 {
		sections = splitAtLocations(text, locs)
	} else if locs := htmlHeadingRe.FindAllStringIndex(text, -1); len(locs) > 1 {
		sections = splitAtLocations(text, locs)
	} else {
		// No headings found -- split by paragraph breaks.
		sections = splitByParagraphs(text)
	}

	if len(sections) <= 1 {
		// No useful split found. Return nil so caller can fall back.
		return nil
	}

	// Enforce size constraints.
	sections = enforceSizes(sections, minChars, maxChars)

	if len(sections) <= 1 {
		return nil
	}

	return sections
}

// splitAtLocations splits text at the given heading locations.
// Each location marks the start of a new section.
func splitAtLocations(text string, locs [][]int) []Section {
	var sections []Section

	// Content before the first heading (preamble).
	if locs[0][0] > 0 {
		preamble := strings.TrimSpace(text[:locs[0][0]])
		if len(preamble) > 0 {
			sections = append(sections, Section{Text: preamble})
		}
	}

	for i, loc := range locs {
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(text)
		}

		heading := strings.TrimSpace(text[loc[0]:loc[1]])
		body := strings.TrimSpace(text[loc[0]:end])

		if len(body) > 0 {
			sections = append(sections, Section{
				Heading: cleanHeading(heading),
				Text:    body,
			})
		}
	}

	return sections
}

// splitByParagraphs splits text at double-newline boundaries.
func splitByParagraphs(text string) []Section {
	// Split on double newline (paragraph boundary).
	paragraphs := strings.Split(text, "\n\n")

	var sections []Section
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if len(p) == 0 {
			continue
		}
		sections = append(sections, Section{Text: p})
	}

	return sections
}

// enforceSizes merges short sections and sub-splits long ones.
func enforceSizes(sections []Section, minChars, maxChars int) []Section {
	// First pass: merge short sections with their next neighbor.
	merged := mergeShort(sections, minChars)

	// Second pass: sub-split sections exceeding maxChars.
	var result []Section
	for _, s := range merged {
		if len(s.Text) <= maxChars {
			result = append(result, s)
			continue
		}
		// Sub-split by paragraphs within this section.
		subs := splitByParagraphs(s.Text)
		if len(subs) <= 1 {
			// Single paragraph exceeding maxChars -- split at word boundary.
			for _, chunk := range splitLargeText(s.Text, maxChars) {
				result = append(result, Section{
					Heading: s.Heading,
					Text:    chunk,
				})
			}
			continue
		}
		// Re-merge the sub-paragraphs to get sections within bounds.
		subMerged := mergeShort(subs, minChars)
		// Inherit heading on the first sub-section.
		if len(subMerged) > 0 && s.Heading != "" {
			subMerged[0].Heading = s.Heading
		}
		result = append(result, subMerged...)
	}

	return result
}

// mergeShort merges sections shorter than minChars with their neighbor.
func mergeShort(sections []Section, minChars int) []Section {
	if len(sections) == 0 {
		return nil
	}

	var merged []Section
	current := sections[0]

	for i := 1; i < len(sections); i++ {
		if len(current.Text) < minChars {
			// Merge with next section.
			current.Text = current.Text + "\n\n" + sections[i].Text
			if current.Heading == "" {
				current.Heading = sections[i].Heading
			}
		} else {
			merged = append(merged, current)
			current = sections[i]
		}
	}
	merged = append(merged, current)

	return merged
}

// splitLargeText splits text at word boundaries targeting maxChars.
func splitLargeText(text string, maxChars int) []string {
	var parts []string
	for len(text) > maxChars {
		// Find word boundary near maxChars.
		end := maxChars
		for end > maxChars*4/5 && end < len(text) {
			if text[end] == ' ' || text[end] == '\n' {
				break
			}
			end--
		}
		if end <= maxChars*4/5 {
			end = maxChars // no good boundary found
		}
		parts = append(parts, strings.TrimSpace(text[:end]))
		text = strings.TrimSpace(text[end:])
	}
	if len(text) > 0 {
		parts = append(parts, text)
	}
	return parts
}

// cleanHeading strips markdown/HTML markers from a heading.
func cleanHeading(h string) string {
	// Strip markdown # prefixes.
	h = strings.TrimLeft(h, "# ")
	// Strip HTML tags.
	h = htmlTagRe.ReplaceAllString(h, "")
	// Strip leading numbers like "1. " or "2.3 ".
	h = numberedPrefixRe.ReplaceAllString(h, "")
	return strings.TrimSpace(h)
}

var (
	htmlTagRe        = regexp.MustCompile(`<[^>]+>`)
	numberedPrefixRe = regexp.MustCompile(`^\d+\.(\d+\.?)* `)
)
