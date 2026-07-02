package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/testutil"
)

// newBackfillTestEngine builds a per-test engine backed by a temp
// directory with the given author configured. Mirrors
// testutil.NewEngine (volatile storage, flat vector index, no
// providers) but writes an author block into the config so
// eng.Config().Author is populated.
func newBackfillTestEngine(t *testing.T, authorName, authorEmail string) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Author.Name = authorName
	cfg.Author.Email = authorEmail
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := eng.Close(); err != nil {
			t.Logf("engine close: %v", err)
		}
	})
	return eng
}

// addBackfillNode inserts a node with the given extra properties.
// Used for nodes the testutil.Record builder can't express (curation
// node_type values, a pre-existing author property).
func addBackfillNode(t *testing.T, eng *core.Engine, extra graph.Properties) string {
	t.Helper()
	props := graph.Properties{
		"content_full":      graph.StringProperty("backfill test content"),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	for k, v := range extra {
		props[k] = v
	}
	eng.Lock()
	defer eng.Unlock()
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	if _, err := eng.Save("test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	return n.ID
}

// authorOf reads back the author property of a node.
func authorOf(t *testing.T, eng *core.Engine, id string) (string, bool) {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatalf("node %s not found", id)
	}
	return n.Properties.GetString("author")
}

func TestBackfillAuthorDryRunCountsAndWritesNothing(t *testing.T) {
	eng := newBackfillTestEngine(t, "Ada Lovelace", "ada@example.com")

	plain := testutil.Record("plain record without author").AddDirect(eng)
	concept := addBackfillNode(t, eng, graph.Properties{
		"node_type": graph.StringProperty("concept"),
	})
	stamped := addBackfillNode(t, eng, graph.Properties{
		"author": graph.StringProperty("someone else"),
	})

	author, err := resolveBackfillAuthor("", eng.Config().Author.String())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if author != "Ada Lovelace <ada@example.com>" {
		t.Fatalf("resolved author = %q, want composed config author", author)
	}

	plan, err := executeAuthorBackfill(eng, author, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if len(plan.stampAuthor) != 1 {
		t.Errorf("stampAuthor = %d, want 1", len(plan.stampAuthor))
	}
	if len(plan.stampCuration) != 1 {
		t.Errorf("stampCuration = %d, want 1", len(plan.stampCuration))
	}
	if plan.alreadyStamped != 1 {
		t.Errorf("alreadyStamped = %d, want 1", plan.alreadyStamped)
	}
	if plan.total != 3 {
		t.Errorf("total = %d, want 3", plan.total)
	}

	// Dry run must not write: author still absent on the unstamped nodes.
	for _, id := range []string{plain, concept} {
		if v, ok := authorOf(t, eng, id); ok {
			t.Errorf("dry run wrote author %q to node %s", v, id)
		}
	}
	if v, _ := authorOf(t, eng, stamped); v != "someone else" {
		t.Errorf("dry run changed pre-existing author to %q", v)
	}
}

func TestBackfillAuthorRealRunStampsOnlyMissingAndIsIdempotent(t *testing.T) {
	eng := newBackfillTestEngine(t, "Ada Lovelace", "ada@example.com")

	plain := testutil.Record("plain record without author").AddDirect(eng)
	stamped := addBackfillNode(t, eng, graph.Properties{
		"author": graph.StringProperty("someone else"),
	})

	author, err := resolveBackfillAuthor("", eng.Config().Author.String())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	plan, err := executeAuthorBackfill(eng, author, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(plan.stampAuthor) != 1 || len(plan.stampCuration) != 0 || plan.alreadyStamped != 1 {
		t.Fatalf("plan = %+v, want 1 stamped / 0 curation / 1 skipped", plan)
	}

	if v, ok := authorOf(t, eng, plain); !ok || v != "Ada Lovelace <ada@example.com>" {
		t.Errorf("plain node author = %q (present=%v), want configured author", v, ok)
	}
	// Pre-existing author keeps its original value (set-once).
	if v, _ := authorOf(t, eng, stamped); v != "someone else" {
		t.Errorf("pre-stamped node author = %q, want original value kept", v)
	}

	// Second run is a no-op: nothing left to stamp.
	plan2, err := executeAuthorBackfill(eng, author, false)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if plan2.stampCount() != 0 {
		t.Errorf("second run stamped %d nodes, want 0", plan2.stampCount())
	}
	if plan2.alreadyStamped != plan2.total {
		t.Errorf("second run: alreadyStamped = %d, total = %d, want equal", plan2.alreadyStamped, plan2.total)
	}
}

func TestBackfillAuthorCurationNodesGetCurationValue(t *testing.T) {
	eng := newBackfillTestEngine(t, "Ada Lovelace", "ada@example.com")

	plain := testutil.Record("plain record without author").AddDirect(eng)
	// node_type values curation stamps at creation time:
	// "concept" (curation/deterministic.go concept emergence) and
	// "observation" (curation/observe.go observation extraction).
	concept := addBackfillNode(t, eng, graph.Properties{
		"node_type": graph.StringProperty("concept"),
	})
	observation := addBackfillNode(t, eng, graph.Properties{
		"node_type": graph.StringProperty("observation"),
	})

	plan, err := executeAuthorBackfill(eng, "Ada Lovelace <ada@example.com>", false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(plan.stampAuthor) != 1 || len(plan.stampCuration) != 2 {
		t.Fatalf("plan = %+v, want 1 author-stamped / 2 curation-stamped", plan)
	}

	if v, _ := authorOf(t, eng, plain); v != "Ada Lovelace <ada@example.com>" {
		t.Errorf("plain node author = %q, want configured author", v)
	}
	for _, id := range []string{concept, observation} {
		if v, _ := authorOf(t, eng, id); v != "curation" {
			t.Errorf("curation-created node %s author = %q, want \"curation\"", id, v)
		}
	}
}

func TestBackfillAuthorNoConfiguredAuthorNoFlagErrors(t *testing.T) {
	eng := newBackfillTestEngine(t, "", "")

	if got := eng.Config().Author.String(); got != "" {
		t.Fatalf("expected blank composed author, got %q", got)
	}
	_, err := resolveBackfillAuthor("", eng.Config().Author.String())
	if err == nil {
		t.Fatal("expected error when no author is configured and no --author flag given")
	}
	if !strings.Contains(err.Error(), "--author") || !strings.Contains(err.Error(), "author.name") {
		t.Errorf("error %q should point at author.name config and the --author flag", err)
	}
}

func TestBackfillAuthorFlagOverridesConfig(t *testing.T) {
	eng := newBackfillTestEngine(t, "Ada Lovelace", "ada@example.com")

	plain := testutil.Record("plain record without author").AddDirect(eng)

	// Flag value wins over the configured author and is used verbatim.
	author, err := resolveBackfillAuthor("Custom Author <custom@example.com>", eng.Config().Author.String())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if author != "Custom Author <custom@example.com>" {
		t.Fatalf("resolved author = %q, want the flag value verbatim", author)
	}

	if _, err := executeAuthorBackfill(eng, author, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if v, _ := authorOf(t, eng, plain); v != "Custom Author <custom@example.com>" {
		t.Errorf("node author = %q, want the flag value", v)
	}

	// A flag also satisfies resolution when nothing is configured.
	author, err = resolveBackfillAuthor("Just A Flag", "")
	if err != nil || author != "Just A Flag" {
		t.Errorf("resolve with flag only = (%q, %v), want flag value and no error", author, err)
	}
}

// TestBackfillAuthorCommandRegistered protects the CLI wiring: the
// backfill parent must hang off root, author must hang off backfill,
// and the two flags must exist. Mirrors repair_test.go's
// flag-registration tests.
func TestBackfillAuthorCommandRegistered(t *testing.T) {
	found := false
	for _, sub := range rootCmd.Commands() {
		if sub == backfillCmd {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("backfill command not registered on rootCmd")
	}

	foundAuthor := false
	for _, sub := range backfillCmd.Commands() {
		if sub == backfillAuthorCmd {
			foundAuthor = true
			break
		}
	}
	if !foundAuthor {
		t.Fatal("author subcommand not registered on backfillCmd")
	}

	if f := backfillAuthorCmd.Flags().Lookup("dry-run"); f == nil {
		t.Error("dry-run flag not registered on backfill author")
	} else if f.Value.Type() != "bool" {
		t.Errorf("dry-run flag type = %q, want bool", f.Value.Type())
	}
	if f := backfillAuthorCmd.Flags().Lookup("author"); f == nil {
		t.Error("author flag not registered on backfill author")
	} else if f.Value.Type() != "string" {
		t.Errorf("author flag type = %q, want string", f.Value.Type())
	}
}
