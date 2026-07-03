package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newStoreAddCmd builds an isolated command carrying the exact flag set
// `store add` registers, so the request-building logic can be exercised
// without touching the real command tree or a server.
func newStoreAddCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "add <name>"}
	addStoreAddFlags(cmd)
	return cmd
}

// TestStoreAddHasNoReadOnlyFlag pins the contract that `store add` never
// exposes --read-only: a top-up preserves the target's existing frozen
// state rather than choosing a new one. It DOES carry every seed flag.
func TestStoreAddHasNoReadOnlyFlag(t *testing.T) {
	cmd := newStoreAddCmd()
	if cmd.Flags().Lookup("read-only") != nil {
		t.Error("`store add` must NOT register a --read-only flag")
	}
	for _, name := range []string{"from-id", "from-collection", "query", "keywords", "meta", "heads-only", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`store add` missing expected flag --%s", name)
		}
	}
}

// TestStoreAddSeedFlagsTriggerCarve asserts carveRequested (reused from
// the create path) recognizes each seed flag on the add command, so a
// seedless `store add` can be rejected with a friendly error.
func TestStoreAddSeedFlagsTriggerCarve(t *testing.T) {
	t.Run("bare_is_not_a_carve", func(t *testing.T) {
		if carveRequested(newStoreAddCmd()) {
			t.Fatal("bare `store add <name>` must be rejected (no seed)")
		}
	})
	cases := map[string]string{
		"from-id":         "01ABC",
		"from-collection": "tasks",
		"query":           "auth design",
		"keywords":        "auth,security",
		"temporality":     "durable",
		"match":           "RWMutex",
		"since":           "2026-01-01",
	}
	for flag, val := range cases {
		t.Run(flag, func(t *testing.T) {
			cmd := newStoreAddCmd()
			if err := cmd.Flags().Set(flag, val); err != nil {
				t.Fatal(err)
			}
			if !carveRequested(cmd) {
				t.Fatalf("seed flag %q must be recognized on `store add`", flag)
			}
		})
	}
}

// TestStoreAddRejectsSelfAdd pins the self-add guard: `gramaton --store foo
// store add foo` (destination == the active SOURCE store) must be rejected
// with a clear message BEFORE any server call, rather than auto-starting
// foo's server and then deadlocking on bbolt's file lock when the api opens
// a second engine on foo's own data dir. Falsifiable: without the
// destIsActiveSource guard, runStoreAdd would fall through to serverPostSlow.
func TestStoreAddRejectsSelfAdd(t *testing.T) {
	base := t.TempDir()
	addNamedStore(t, base, "self") // creates stores/self/{data,config.yaml}

	oldCfg, oldStore := cfgDir, storeName
	t.Cleanup(func() { cfgDir, storeName = oldCfg, oldStore })
	cfgDir = base
	storeName = "self" // the active SOURCE store
	t.Setenv("GRAMATON_STORE", "")

	cmd := newStoreAddCmd()
	if err := cmd.Flags().Set("from-id", "01ABC"); err != nil {
		t.Fatalf("set from-id: %v", err)
	}

	err := runStoreAdd(cmd, []string{"self"})
	if err == nil {
		t.Fatal("expected `store add self` (into the active store itself) to be rejected")
	}
	if !strings.Contains(err.Error(), "into itself") {
		t.Fatalf("error = %q, want it to explain a store cannot be added into itself", err)
	}
}

// TestBuildCarveAddRequestMapsFlags proves the flag -> CarveAddRequest
// mapping: every seed, every query filter, and every option lands in the
// right field, and the destination is the absolute stores/<name>/data path.
func TestBuildCarveAddRequestMapsFlags(t *testing.T) {
	cmd := newStoreAddCmd()
	set := func(flag, val string) {
		if err := cmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set %s: %v", flag, err)
		}
	}
	set("from-id", "01AAA,01BBB")
	set("from-id", "01CCC") // repeatable appends
	set("from-collection", "tasks,notes")
	set("query", "auth design")
	set("match", "RWMutex")
	set("keywords", "auth,security")
	set("temporality", "durable")
	set("knowledge-type", "semantic")
	set("epistemic-status", "well_established")
	set("resolution", "unresolved")
	set("since", "2026-01-01")
	set("meta", "sprint=23")
	set("meta", "assignee=Sarah")
	set("heads-only", "true")
	set("dry-run", "true")

	req := buildCarveAddRequest(cmd, "shared")

	if req.DestName != "shared" {
		t.Errorf("DestName = %q, want shared", req.DestName)
	}
	wantSuffix := filepath.Join("stores", "shared", "data")
	if !filepath.IsAbs(req.DestDataDir) {
		t.Errorf("DestDataDir = %q, want an absolute path", req.DestDataDir)
	}
	if filepath.Base(filepath.Dir(req.DestDataDir)) != "shared" ||
		len(req.DestDataDir) < len(wantSuffix) ||
		req.DestDataDir[len(req.DestDataDir)-len(wantSuffix):] != wantSuffix {
		t.Errorf("DestDataDir = %q, want it to end with %q", req.DestDataDir, wantSuffix)
	}

	if len(req.IDs) != 3 || req.IDs[0] != "01AAA" || req.IDs[2] != "01CCC" {
		t.Errorf("IDs = %v, want [01AAA 01BBB 01CCC]", req.IDs)
	}
	if len(req.Collections) != 2 || req.Collections[0] != "tasks" || req.Collections[1] != "notes" {
		t.Errorf("Collections = %v, want [tasks notes]", req.Collections)
	}
	if req.Text != "auth design" {
		t.Errorf("Text = %q, want 'auth design'", req.Text)
	}
	if req.Match != "RWMutex" {
		t.Errorf("Match = %q, want RWMutex", req.Match)
	}
	if len(req.Keywords) != 2 || req.Keywords[0] != "auth" || req.Keywords[1] != "security" {
		t.Errorf("Keywords = %v, want [auth security]", req.Keywords)
	}
	if req.Temporality != "durable" {
		t.Errorf("Temporality = %q, want durable", req.Temporality)
	}
	if req.KnowledgeType != "semantic" {
		t.Errorf("KnowledgeType = %q, want semantic", req.KnowledgeType)
	}
	if req.EpistemicStatus != "well_established" {
		t.Errorf("EpistemicStatus = %q, want well_established", req.EpistemicStatus)
	}
	if req.Resolution != "unresolved" {
		t.Errorf("Resolution = %q, want unresolved", req.Resolution)
	}
	if req.Since != "2026-01-01" {
		t.Errorf("Since = %q, want 2026-01-01", req.Since)
	}
	if req.Meta["sprint"] != "23" || req.Meta["assignee"] != "Sarah" {
		t.Errorf("Meta = %v, want {sprint:23 assignee:Sarah}", req.Meta)
	}
	if !req.HeadsOnly {
		t.Error("HeadsOnly should be true")
	}
	if !req.DryRun {
		t.Error("DryRun should be true")
	}
}
