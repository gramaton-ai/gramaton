package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/gramaton-ai/gramaton/api"
)

// TestChangelogRescopesOnCheckout pins the incremental rebuild: a
// version minted on an abandoned lineage is retracted when checking
// out a branch from before it, and replayed when checking the
// lineage back out.
func TestChangelogRescopesOnCheckout(t *testing.T) {
	srv, eng := setupTestServer(t)
	ctx := context.Background()

	recID := addRecord(t, eng, "record with a branched history")

	if _, e := srv.api.BranchCreate(ctx, api.BranchCreateRequest{Name: "before-edit"}); e != nil {
		t.Fatalf("BranchCreate: %v", e)
	}

	// Edit on main AFTER the branch point: a second logical version.
	eng.Lock()
	eng.SetContentProp(recID, "content_full", "revised on main")
	if _, err := eng.Save("revise on main"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	if got := len(eng.Changelog().Versions(recID)); got != 2 {
		t.Fatalf("versions on main = %d, want 2", got)
	}

	if _, e := srv.api.BranchCheckout(ctx, "before-edit"); e != nil {
		t.Fatalf("BranchCheckout before-edit: %v", e)
	}
	if got := len(eng.Changelog().Versions(recID)); got != 1 {
		t.Fatalf("versions on before-edit = %d, want 1 (main's revision retracted)", got)
	}

	if _, e := srv.api.BranchCheckout(ctx, "main"); e != nil {
		t.Fatalf("BranchCheckout main: %v", e)
	}
	if got := len(eng.Changelog().Versions(recID)); got != 2 {
		t.Fatalf("versions back on main = %d, want 2 (replayed)", got)
	}
	if eng.Changelog().Marker() != eng.HeadHash() {
		t.Fatal("marker did not land on HEAD after the round trip")
	}
}

// TestChangelogIndexesRevertCommit pins the adopted-graph seam: the
// revert commit's tree differs wholesale from its parent while the
// adopted graph carries no dirty nodes, so its logical versions come
// from the explicit tree diff -- the reverted record gains a third
// version (its content changed back).
func TestChangelogIndexesRevertCommit(t *testing.T) {
	srv, eng := setupTestServer(t)

	recID := addRecord(t, eng, "originally worded record")
	eng.RLock()
	target := eng.HeadHash()
	eng.RUnlock()

	eng.Lock()
	eng.SetContentProp(recID, "content_full", "edited wording")
	if _, err := eng.Save("edit"); err != nil {
		eng.Unlock()
		t.Fatalf("Save: %v", err)
	}
	eng.Unlock()

	w := doRequest(t, srv, "POST", "/v1/revert", map[string]any{"hash": target})
	if w.Code != http.StatusOK {
		t.Fatalf("revert: %d: %s", w.Code, w.Body.String())
	}

	versions := eng.Changelog().Versions(recID)
	if len(versions) != 3 {
		t.Fatalf("versions after revert = %d (%+v), want 3 (create, edit, revert-back)", len(versions), versions)
	}
	if versions[2].NodeHash == "" {
		t.Fatal("revert entry must carry the restored content hash, not a deletion mark")
	}
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(recID)
	if c, _ := n.Properties.GetString("content_full"); c != "originally worded record" {
		t.Fatalf("content after revert = %q", c)
	}
}
