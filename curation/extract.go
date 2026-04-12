// Package curation contains the observation extraction pipeline (D18, D23).
//
// Extraction uses deterministic TF-IDF sentence scoring to select the
// most informative sentences from a record's content. No LLM required
// for the base extraction -- LLM refinement is an optional second pass.
package curation

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Observation is a single extracted fact or key sentence from a parent
// record's content.
type Observation struct {
	Text  string  // the extracted sentence or fact
	Score float64 // TF-IDF importance score (higher = more informative)
}

// ExtractObservations selects the most informative sentences from content
// using TF-IDF scoring. Returns up to maxObs observations, sorted by
// their order of appearance in the text (not by score).
//
// Sliding scale (D23): maxObs = min(len(content)/1000, cap).
// Records <500 chars return nil (the record itself is the observation).
func ExtractObservations(content string, maxCap int) []Observation {
	if len(content) < 500 {
		return nil
	}
	if maxCap <= 0 {
		maxCap = 20
	}

	maxObs := len(content) / 1000
	if maxObs < 1 {
		maxObs = 1
	}
	if maxObs > maxCap {
		maxObs = maxCap
	}

	sentences := splitSentences(content)
	if len(sentences) <= maxObs {
		// Fewer sentences than slots -- return all.
		obs := make([]Observation, len(sentences))
		for i, s := range sentences {
			obs[i] = Observation{Text: s.text, Score: 1.0}
		}
		return obs
	}

	// Compute TF-IDF scores for each sentence.
	scored := tfidfScore(sentences)

	// Select top maxObs by score.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	selected := scored[:maxObs]

	// Re-sort by original position (preserve document order).
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].pos < selected[j].pos
	})

	obs := make([]Observation, len(selected))
	for i, s := range selected {
		obs[i] = Observation{Text: s.text, Score: s.score}
	}
	return obs
}

// MaxObservations returns the observation count for a given content
// length, following the D23 sliding scale.
func MaxObservations(contentLen, maxCap int) int {
	if contentLen < 500 {
		return 0
	}
	if maxCap <= 0 {
		maxCap = 20
	}
	n := contentLen / 1000
	if n < 1 {
		n = 1
	}
	if n > maxCap {
		n = maxCap
	}
	return n
}

type sentence struct {
	text string
	pos  int // position in the original text (for order preservation)
}

type scoredSentence struct {
	text  string
	pos   int
	score float64
}

// splitSentences splits text into sentences using punctuation boundaries.
// Filters out very short sentences (<20 chars) and very long ones (>2000 chars).
func splitSentences(text string) []sentence {
	var sentences []sentence
	var current strings.Builder
	pos := 0

	for i, r := range text {
		current.WriteRune(r)

		// Sentence boundary: period, exclamation, question mark followed
		// by whitespace or end of text.
		if (r == '.' || r == '!' || r == '?') && (i+1 >= len(text) || unicode.IsSpace(rune(text[i+1]))) {
			s := strings.TrimSpace(current.String())
			if len(s) >= 20 && len(s) <= 2000 {
				sentences = append(sentences, sentence{text: s, pos: pos})
			}
			pos++
			current.Reset()
		}
	}

	// Handle trailing text without terminal punctuation.
	if s := strings.TrimSpace(current.String()); len(s) >= 20 && len(s) <= 2000 {
		sentences = append(sentences, sentence{text: s, pos: pos})
	}

	return sentences
}

// tfidfScore computes a TF-IDF-inspired importance score for each sentence.
// IDF is computed across sentences in the document (not a global corpus).
// Sentences with rare, distinctive terms score higher.
func tfidfScore(sentences []sentence) []scoredSentence {
	// Tokenize all sentences.
	tokenized := make([][]string, len(sentences))
	for i, s := range sentences {
		tokenized[i] = tokenizeLower(s.text)
	}

	// Document frequency: how many sentences contain each term.
	df := make(map[string]int)
	for _, tokens := range tokenized {
		seen := make(map[string]bool)
		for _, t := range tokens {
			if !seen[t] {
				df[t]++
				seen[t] = true
			}
		}
	}

	n := float64(len(sentences))
	scored := make([]scoredSentence, len(sentences))

	for i, tokens := range tokenized {
		if len(tokens) == 0 {
			scored[i] = scoredSentence{text: sentences[i].text, pos: sentences[i].pos}
			continue
		}

		// Term frequency within this sentence.
		tf := make(map[string]int)
		for _, t := range tokens {
			tf[t]++
		}

		// Sum of TF-IDF for all terms in this sentence.
		var score float64
		for term, count := range tf {
			idf := math.Log(1 + n/float64(df[term]))
			score += float64(count) * idf
		}
		// Normalize by sentence length to avoid biasing toward long sentences.
		score /= float64(len(tokens))

		scored[i] = scoredSentence{
			text:  sentences[i].text,
			pos:   sentences[i].pos,
			score: score,
		}
	}

	return scored
}

// tokenizeLower splits text into lowercase tokens, filtering short words
// and common stop words.
func tokenizeLower(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) >= 3 && !isStopWord(w) {
			out = append(out, w)
		}
	}
	return out
}

// isStopWord returns true for common English stop words that don't
// contribute to TF-IDF discrimination.
func isStopWord(w string) bool {
	switch w {
	case "the", "and", "for", "are", "but", "not", "you", "all",
		"can", "had", "her", "was", "one", "our", "out", "has",
		"have", "been", "from", "this", "that", "with", "they",
		"will", "each", "make", "like", "just", "over", "such",
		"also", "into", "than", "them", "then", "only", "its",
		"about", "which", "when", "what", "there", "their",
		"would", "could", "should", "these", "other", "some",
		"very", "after", "before", "being", "between", "does",
		"more", "most", "were", "while":
		return true
	}
	return false
}
