package curation

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestPickOlderTimestampDifference exercises the easy path: one
// record clearly older than the other. pickOlder must return the
// older-by-time as the historical record regardless of ID order.
func TestPickOlderTimestampDifference(t *testing.T) {
	g := graph.New()
	a := g.AddNode(graph.Properties{"content_full": graph.StringProperty("a")})
	b := g.AddNode(graph.Properties{"content_full": graph.StringProperty("b")})

	now := time.Now().UTC()
	old := now.Add(-time.Hour)

	if olderID, _ := pickOlder(g, a.ID, b.ID, old, now); olderID != a.ID {
		t.Errorf("a older: got olderID=%q, want %q", olderID, a.ID)
	}
	if olderID, _ := pickOlder(g, a.ID, b.ID, now, old); olderID != b.ID {
		t.Errorf("b older: got olderID=%q, want %q", olderID, b.ID)
	}
}

// TestPickOlderTieBreakOnInboundEdges is the regression test for
// P1-36: identical created_at must NOT silently keep pair.IDA as
// "older" (which is just lex order). Tie-break on inbound edge
// count, keeping the more-referenced record alive.
func TestPickOlderTieBreakOnInboundEdges(t *testing.T) {
	g := graph.New()
	a := g.AddNode(graph.Properties{"content_full": graph.StringProperty("a")})
	b := g.AddNode(graph.Properties{"content_full": graph.StringProperty("b")})
	// Two extra nodes that point at b.
	r1 := g.AddNode(graph.Properties{"content_full": graph.StringProperty("r1")})
	r2 := g.AddNode(graph.Properties{"content_full": graph.StringProperty("r2")})
	if _, err := g.AddEdge(r1.ID, b.ID, "related_to", 0.5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddEdge(r2.ID, b.ID, "related_to", 0.5, nil); err != nil {
		t.Fatal(err)
	}

	// a has 0 inbound, b has 2 inbound. With identical timestamps,
	// a (less-referenced) must be picked as older.
	now := time.Now().UTC()
	olderID, newerID := pickOlder(g, a.ID, b.ID, now, now)
	if olderID != a.ID {
		t.Errorf("expected a (0 inbound) as older, got %q", olderID)
	}
	if newerID != b.ID {
		t.Errorf("expected b (2 inbound) as newer survivor, got %q", newerID)
	}
}

// TestPickOlderFinalFallbackIsLexOrder confirms that when both
// timestamps AND inbound edge counts are equal, the result is
// lex-order. This is just the "no signal at all" deterministic
// floor.
func TestPickOlderFinalFallbackIsLexOrder(t *testing.T) {
	g := graph.New()
	a := g.AddNode(graph.Properties{"content_full": graph.StringProperty("a")})
	b := g.AddNode(graph.Properties{"content_full": graph.StringProperty("b")})

	now := time.Now().UTC()
	olderID, newerID := pickOlder(g, a.ID, b.ID, now, now)
	// Whichever is lex-smaller becomes "older" by the final fallback.
	if a.ID < b.ID {
		if olderID != a.ID || newerID != b.ID {
			t.Errorf("lex fallback: olderID=%q newerID=%q (a.ID=%q b.ID=%q)", olderID, newerID, a.ID, b.ID)
		}
	} else {
		if olderID != b.ID || newerID != a.ID {
			t.Errorf("lex fallback: olderID=%q newerID=%q (a.ID=%q b.ID=%q)", olderID, newerID, a.ID, b.ID)
		}
	}
	// Always: olderID + newerID is the set {a, b}.
	if olderID == newerID {
		t.Fatal("pickOlder returned the same ID twice")
	}
}
