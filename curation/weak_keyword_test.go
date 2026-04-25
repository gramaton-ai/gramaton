package curation

import "testing"

// TestIsWeakConceptKeyword pins the denylist so future additions /
// removals are caught by review. The denylist is what filters
// keywords out of concept clustering before LLM synthesis runs;
// shrinking it leaks generic terms (e.g. "context") into clusters
// and produces muddled syntheses.
func TestIsWeakConceptKeyword(t *testing.T) {
	cases := []struct {
		kw   string
		want bool
	}{
		// Length floor.
		{"", true},
		{"a", true},
		{"ab", true},
		{"abc", false}, // legit 3-char term

		// Source/structure meta-terms (existing).
		{"article", true}, {"section", true}, {"overview", true},
		{"summary", true}, {"reference", true}, {"document", true},
		{"note", true}, {"notes", true}, {"todo", true},

		// Generic LLM/agent vocabulary (P2-09 fix #5 additions).
		// These were the load-bearing leak: every other record
		// captured during agent flows mentions "context" or
		// "content", so without filtering they dominated concept
		// clusters with no signal.
		{"context", true},
		{"content", true},
		{"system", true},

		// Case-insensitive (the denylist is lowercased internally).
		{"Context", true}, {"CONTENT", true}, {"System", true},

		// Real concept keywords must NOT be flagged.
		{"kafka", false},
		{"bayesian", false},
		{"prolly tree", false},
		{"embedding", false},
		{"curation", false},
	}
	for _, tc := range cases {
		t.Run(tc.kw, func(t *testing.T) {
			got := isWeakConceptKeyword(tc.kw)
			if got != tc.want {
				t.Errorf("isWeakConceptKeyword(%q) = %v, want %v", tc.kw, got, tc.want)
			}
		})
	}
}
