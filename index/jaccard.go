package index

import (
	"strings"
	"unicode"
)

// Tokenize splits text into lowercase word tokens, discarding
// non-alphanumeric characters. Suitable for Jaccard comparison.
func Tokenize(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) >= 2 { // skip single-char tokens
			out = append(out, w)
		}
	}
	return out
}

// JaccardSimilarity computes the Jaccard index between two token sets:
// |intersection| / |union|. Returns 0 if both sets are empty.
func JaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}

	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}

	intersection := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
