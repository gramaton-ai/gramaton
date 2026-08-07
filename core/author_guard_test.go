package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestAuthorStampCoversEveryAddNodeSite pins the author-attribution
// contract: every node-creation site in production code has made an
// explicit decision about the set-once `author` property. The walk
// finds every `.AddNode(` call site in non-test .go files and compares
// the per-file counts against the annotated expectation list below.
//
// Categories (each entry in the list carries one):
//
//   - STAMPED: the site stamps the composed config identity
//     (engine.Config().Author.String()); empty identity stamps nothing.
//   - CURATION: system-created node; stamps the curation.NodeAuthor
//     constant ("curation") unconditionally.
//   - INHERITS: sub-node copies the parent record's author (chunking's
//     inheritedMetadataKeys); config is never consulted.
//   - PRESERVES: import path keeps the source row's author via the
//     safePropTypes allowlist; absent stays absent, and the importer's
//     identity is never re-stamped.
//   - EXEMPT: test fixture builders in testutil/; fixtures stay
//     unstamped by design.
//   - DEFINITION: the file defines the AddNode methods themselves;
//     a method definition carries no receiver dot, so the `.AddNode(`
//     pattern matches nothing. Pinned at 0 so any CALL added to the
//     file trips the count.
//   - DELEGATE: pass-through wrapper; the author decision belongs to
//     each of the wrapper's callers, which the walk counts separately.
//
// If this test fails because a NEW `.AddNode(` call site appeared:
//  1. Decide the site's category. A user-initiated create path must
//     stamp the composed config author into the props map BEFORE
//     AddNode (so it lands in the property index); a curation-created
//     node must stamp the "curation" constant; a derived sub-node
//     should inherit; an import should preserve.
//  2. Implement the stamp (see api/save.go for the canonical form).
//  3. Add the file with its new count to the expectation map below,
//     annotated with the category and a one-line justification.
//
// Never add an entry without the category annotation -- the list is
// the audit trail for who writes `author` and why.
//
// Residual limits, acknowledged: the tripwire is grep-level. Per-file
// counts cannot see a same-file swap of WHICH call site is stamped
// (two sites in one file trading their stamps keeps the count
// constant), and node creation that bypasses the `.AddNode(` spelling
// entirely -- constructing Node values directly, as the method bodies
// in graph/node.go do -- is invisible to it. Behavioral coverage of
// the stamps themselves lives in the author integration tests.
func TestAuthorStampCoversEveryAddNodeSite(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("repo root not found from test dir: %v", err)
	}

	// file -> expected number of `.AddNode(` call sites. Every entry
	// is annotated with its author-attribution category.
	expected := map[string]int{
		// STAMPED: single-record capture; stamps composed config author.
		filepath.Join("api", "save.go"): 1,
		// STAMPED: sync batch capture; author composed once per batch.
		filepath.Join("api", "save_batch.go"): 1,
		// STAMPED: chunked async batch capture; composed once per chunk.
		filepath.Join("api", "save_batch_chunked.go"): 1,
		// STAMPED x3: collection create, single item add, batch item
		// add; batch composes once before the loop.
		filepath.Join("api", "collections.go"): 3,
		// STAMPED x5: session node, topic node, segment node, promoted
		// Memory record, and resolve_held's allow_similar promotion;
		// SessionSave and SessionResolveHeld each compose once per commit.
		filepath.Join("api", "sessions.go"): 5,
		// STAMPED: server-side save service (intake + legacy HTTP save).
		filepath.Join("server", "service_records.go"): 1,
		// STAMPED: /v1/ingest file ingestion; composed once per request.
		filepath.Join("server", "handler_ops.go"): 1,
		// CURATION: emerged concept nodes carry the "curation" constant.
		filepath.Join("curation", "deterministic.go"): 1,
		// CURATION: observation nodes carry the "curation" constant.
		filepath.Join("curation", "observe.go"): 1,
		// INHERITS x2: section sub-nodes inherit the parent's author via
		// inheritedMetadataKeys; legacy dumb chunks inherit no metadata
		// at all (pre-existing design), so no stamp there either.
		filepath.Join("chunking", "chunking.go"): 2,
		// PRESERVES x2 (JSONL line 170-ish, CSV line 322-ish): source
		// row's author survives via safePropTypes; absent stays absent.
		// STAMPED x1 (Obsidian): user importing their own vault gets the
		// configured author.
		filepath.Join("backup", "import.go"): 3,
		// EXEMPT x3: populated-store test fixture builders.
		filepath.Join("testutil", "populated.go"): 3,
		// EXEMPT: generic record fixture builder.
		filepath.Join("testutil", "testutil.go"): 1,
		// DEFINITION x0: Graph.AddNode and AddNodeWithIDForTest are
		// defined here; both bodies construct the Node struct directly,
		// so no `.AddNode(` call appears. A new production call site
		// added to this file breaks the 0 count.
		filepath.Join("graph", "node.go"): 0,
		// DELEGATE: WriteSession.AddNode's single forward to
		// ws.engine.graph.AddNode(props). Not a decision point -- the
		// author decision belongs to each WriteSession.AddNode caller.
		filepath.Join("core", "write_session.go"): 1,
	}

	found := map[string]int{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if n := strings.Count(string(data), ".AddNode("); n > 0 {
			found[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	var problems []string
	for rel, n := range found {
		want, ok := expected[rel]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %d unexpected .AddNode( call site(s)", rel, n))
			continue
		}
		if n != want {
			problems = append(problems, fmt.Sprintf("%s: %d .AddNode( call site(s), expected %d", rel, n, want))
		}
	}
	for rel, want := range expected {
		if want == 0 {
			// Zero-count entries (DEFINITION) document a file that must
			// stay free of call sites; absence from found IS the pass.
			continue
		}
		if _, ok := found[rel]; !ok {
			problems = append(problems, fmt.Sprintf("%s: expected %d .AddNode( call site(s), found none (stale expectation entry?)", rel, want))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("author-attribution tripwire hit:\n  %s\n\n"+
			"Every .AddNode( call site must make an explicit author decision.\n"+
			"New site: stamp it (STAMPED via engine.Config().Author.String(),\n"+
			"CURATION constant, INHERITS from parent, or PRESERVES from source),\n"+
			"then add the file+count to the expectation map in this test with\n"+
			"a category annotation. Removed/moved site: update the map so the\n"+
			"audit trail follows the code.",
			strings.Join(problems, "\n  "))
	}
}
