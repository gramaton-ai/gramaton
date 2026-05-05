package curation

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestVerifyDedupJaccardLongContent pins the existing long-content
// threshold of 0.3 (single shared structural term keeps long pairs
// from auto-consolidating).
func TestVerifyDedupJaccardLongContent(t *testing.T) {
	long := func(prefix string) string {
		// Padding to push len ≥ 200 chars so the long-content path
		// exercises.
		return prefix + " lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua ut enim ad minim veniam quis nostrud exercitation ullamco laboris."
	}

	cases := []struct {
		name     string
		a, b     string
		want     bool
	}{
		{
			name: "high overlap → consolidate",
			a:    long("Bayesian methods improve calibration"),
			b:    long("Bayesian methods improve calibration"),
			want: true,
		},
		{
			name: "moderate overlap → consolidate (above 0.3)",
			a:    long("Bayesian methods improve calibration"),
			b:    long("Bayesian techniques improve forecasts"),
			want: true,
		},
		{
			name: "low overlap → reject (below 0.3)",
			a:    "engine.go god-object split into providers indexes searcher subsystems with applyToNode consolidator that kills the forgotten-index-update bug class",
			b:    "tokenizer.json declares larger truncation max_length than the model max_position_embeddings causes Forward to panic with slice-bounds-out-of-range errors",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := graph.New()
			a := g.AddNode(graph.Properties{"content_full": graph.StringProperty(tc.a)})
			b := g.AddNode(graph.Properties{"content_full": graph.StringProperty(tc.b)})
			got := verifyDedupJaccard(g, a, b)
			if got != tc.want {
				t.Errorf("verifyDedupJaccard = %v, want %v\n  a=%q\n  b=%q", got, tc.want, tc.a, tc.b)
			}
		})
	}
}

// TestVerifyDedupJaccardShortContentRejectsFalsePositives is the
// load-bearing regression for tracker 01KPEDCPMXR23V1SSGTNXGRS7T:
// short pairs with high cosine similarity but distinct meaning must
// not auto-consolidate. Pre-fix, the function returned true for any
// pair where both sides were <200 chars, so cosine ≥ 0.92 alone was
// enough to trigger supersession. The new threshold (0.5 for short
// content) catches the obvious false-positive shape: two short
// strings sharing one structural keyword.
func TestVerifyDedupJaccardShortContentRejectsFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{
			// Two distinct bug reports both labeled "bug" — pre-fix
			// false-positive shape. Jaccard {auth,bug} ∩ {login,bug}
			// = {bug}; |∪|=3; J=0.33. Below 0.5 → reject.
			name: "two distinct short bugs sharing one keyword → reject",
			a:    "auth bug",
			b:    "login bug",
			want: false,
		},
		{
			// Three-token overlap on a four-word stem. J = 3/5 = 0.6
			// → above 0.5 → consolidate.
			name: "near-duplicate short content → consolidate",
			a:    "kafka stream processing fails",
			b:    "kafka stream processing breaks",
			want: true,
		},
		{
			// Identical short content → consolidate.
			name: "identical short content → consolidate",
			a:    "redis connection timeout",
			b:    "redis connection timeout",
			want: true,
		},
		{
			// Disjoint short content → reject.
			name: "disjoint short content → reject",
			a:    "kafka producer config",
			b:    "redis cluster failover",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := graph.New()
			a := g.AddNode(graph.Properties{"content_full": graph.StringProperty(tc.a)})
			b := g.AddNode(graph.Properties{"content_full": graph.StringProperty(tc.b)})
			got := verifyDedupJaccard(g, a, b)
			if got != tc.want {
				t.Errorf("verifyDedupJaccard = %v, want %v\n  a=%q\n  b=%q", got, tc.want, tc.a, tc.b)
			}
		})
	}
}

// TestVerifyDedupJaccardMixedLengthUsesStricterThreshold pins the
// rule: when EITHER side is short, the stricter (0.5) threshold
// applies. This avoids a long-vs-short pair sneaking past the 0.3
// long-content bar despite the short side being underspecified.
func TestVerifyDedupJaccardMixedLengthUsesStricterThreshold(t *testing.T) {
	short := "auth bug"
	// Long content with deliberately moderate token overlap to the
	// short side: shares "auth" and "bug" but is otherwise a long
	// distinct technical doc. Jaccard against the 2-token short
	// side:  intersection={auth,bug}=2; union ≈ 30+ tokens from
	// long; ratio well below 0.5.
	long := "the auth bug we're tracking has nothing to do with login flow but rather with token expiration during long-running sessions when refresh tokens silently fail and the user is logged out without warning."

	g := graph.New()
	a := g.AddNode(graph.Properties{"content_full": graph.StringProperty(short)})
	b := g.AddNode(graph.Properties{"content_full": graph.StringProperty(long)})

	if verifyDedupJaccard(g, a, b) {
		t.Errorf("mixed-length pair with low token overlap should NOT consolidate (stricter short threshold applies)")
	}
}
