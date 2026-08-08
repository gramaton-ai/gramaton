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
	eng3 := openReadOnlyTestEngine(t, dir)
	ref, err = ReadRef(dir, "main")
	if err != nil {
		t.Fatalf("read stray ref: %v", err)
	}
	if ref != stray {
		t.Fatalf("divergent ref was rewritten to %s; must be left untouched", ref)
	}
	if err := eng3.Close(); err != nil {
		t.Fatalf("close 3: %v", err)
	}

	// A frozen store is a byte-stable artifact: even a genuinely
	// trailing ref (an ancestor of HEAD) is reported, never repaired.
	if err := WriteRef(dir, "main", c1.Hash); err != nil {
		t.Fatalf("rewind ref for frozen leg: %v", err)
	}
	if err := FreezeStore(dir, ""); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	_ = openReadOnlyTestEngine(t, dir)
	ref, err = ReadRef(dir, "main")
	if err != nil {
		t.Fatalf("read frozen ref: %v", err)
	}
	if ref != c1.Hash {
		t.Fatalf("frozen store's trailing ref was rewritten to %s; boot must not write to a read-only store", ref)
	}
}
