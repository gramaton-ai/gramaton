package core

import (
	"testing"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestBootFastForwardsTrailingActiveRef pins the boot repair for the
// crash window between a Save's HEAD write and its branch-ref write:
// on reopen, an active-branch ref that is an ancestor of HEAD
// fast-forwards to HEAD, so a later checkout of the same branch
// cannot silently rewind it. A ref pointing off the chain is real
// divergence and is left untouched.
func TestBootFastForwardsTrailingActiveRef(t *testing.T) {
	dir := newReadOnlyTestDir(t)
	eng := openReadOnlyTestEngine(t, dir)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("v1"),
	})
	c1, err := eng.Save("first")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 1: %v", err)
	}
	eng.SetContentProp(n.ID, "content_full", "v2")
	c2, err := eng.Save("second")
	if err != nil {
		eng.Unlock()
		t.Fatalf("Save 2: %v", err)
	}
	eng.Unlock()
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Simulate the crash: the ref write that should have followed
	// the second HEAD write never landed.
	if err := WriteRef(dir, "main", c1.Hash); err != nil {
		t.Fatalf("rewind ref: %v", err)
	}

	eng2 := openReadOnlyTestEngine(t, dir)
	if got := eng2.HeadHash(); got != c2.Hash {
		t.Fatalf("HEAD after reopen = %s, want %s", got, c2.Hash)
	}
	ref, err := ReadRef(dir, "main")
	if err != nil {
		t.Fatalf("read ref: %v", err)
	}
	if ref != c2.Hash {
		t.Fatalf("ref = %s after reopen, want fast-forwarded to HEAD %s", ref, c2.Hash)
	}
	if err := eng2.Close(); err != nil {
		t.Fatalf("close 2: %v", err)
	}

	// A ref that is not on the chain at all must never be repaired.
	const stray = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := WriteRef(dir, "main", stray); err != nil {
		t.Fatalf("write stray ref: %v", err)
	}
	_ = openReadOnlyTestEngine(t, dir)
	ref, err = ReadRef(dir, "main")
	if err != nil {
		t.Fatalf("read stray ref: %v", err)
	}
	if ref != stray {
		t.Fatalf("divergent ref was rewritten to %s; must be left untouched", ref)
	}
}
