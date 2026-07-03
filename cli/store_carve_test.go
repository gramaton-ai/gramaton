package cli

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// newStoreCreateCmd builds an isolated command carrying the exact flag
// set `store create` registers, so the routing + request-building logic
// can be exercised without touching the real command tree or a server.
func newStoreCreateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create <name>"}
	addStoreCreateFlags(cmd)
	return cmd
}

// TestStoreCreateNoSeedIsOffline pins the regression guard: with no seed
// flag set, `store create` must NOT route to the carve; --read-only and
// --dry-run/--heads-only alone are options, not seeds.
func TestStoreCreateNoSeedIsOffline(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd := newStoreCreateCmd()
		if carveRequested(cmd) {
			t.Fatal("bare `store create <name>` must take the offline path")
		}
	})
	t.Run("read_only_alone", func(t *testing.T) {
		cmd := newStoreCreateCmd()
		if err := cmd.Flags().Set("read-only", "true"); err != nil {
			t.Fatal(err)
		}
		if carveRequested(cmd) {
			t.Fatal("`store create <name> --read-only` must stay the offline empty frozen-store path")
		}
	})
	t.Run("dry_run_alone", func(t *testing.T) {
		cmd := newStoreCreateCmd()
		if err := cmd.Flags().Set("dry-run", "true"); err != nil {
			t.Fatal(err)
		}
		if carveRequested(cmd) {
			t.Fatal("--dry-run alone is not a seed and must not trigger a carve")
		}
	})
}

// TestStoreCreateSeedFlagsTriggerCarve asserts each seed flag flips the
// routing to the carve path.
func TestStoreCreateSeedFlagsTriggerCarve(t *testing.T) {
	cases := map[string]string{
		"from-id":          "01ABC",
		"from-collection":  "tasks",
		"query":            "auth design",
		"keywords":         "auth,security",
		"temporality":      "durable",
		"knowledge-type":   "semantic",
		"epistemic-status": "well_established",
		"resolution":       "unresolved",
		"match":            "RWMutex",
		"since":            "2026-01-01",
	}
	for flag, val := range cases {
		t.Run(flag, func(t *testing.T) {
			cmd := newStoreCreateCmd()
			if err := cmd.Flags().Set(flag, val); err != nil {
				t.Fatal(err)
			}
			if !carveRequested(cmd) {
				t.Fatalf("seed flag %q must route to the carve path", flag)
			}
		})
	}
	// --meta is a StringToString; set it separately.
	t.Run("meta", func(t *testing.T) {
		cmd := newStoreCreateCmd()
		if err := cmd.Flags().Set("meta", "sprint=23"); err != nil {
			t.Fatal(err)
		}
		if !carveRequested(cmd) {
			t.Fatal("seed flag --meta must route to the carve path")
		}
	})
}

// TestBuildCarveRequestMapsFlags proves the flag -> CarveOutRequest field
// mapping: every seed, every query filter, and every option lands in the
// right request field, and the destination is the absolute
// stores/<name>/data path.
func TestBuildCarveRequestMapsFlags(t *testing.T) {
	cmd := newStoreCreateCmd()
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
	set("read-only", "true")
	set("heads-only", "true")
	set("dry-run", "true")

	req := buildCarveRequest(cmd, "shared")

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
	if !req.ReadOnly {
		t.Error("ReadOnly should be true")
	}
	if !req.HeadsOnly {
		t.Error("HeadsOnly should be true")
	}
	if !req.DryRun {
		t.Error("DryRun should be true")
	}
}

// TestBuildCarveRequestDefaults asserts an unseeded-but-single-seed
// request leaves every unset field zero (so the api's own seed/option
// defaults apply, not stray CLI values).
func TestBuildCarveRequestDefaults(t *testing.T) {
	cmd := newStoreCreateCmd()
	if err := cmd.Flags().Set("from-id", "01AAA"); err != nil {
		t.Fatal(err)
	}
	req := buildCarveRequest(cmd, "solo")

	if len(req.IDs) != 1 || req.IDs[0] != "01AAA" {
		t.Errorf("IDs = %v, want [01AAA]", req.IDs)
	}
	if len(req.Collections) != 0 {
		t.Errorf("Collections = %v, want empty", req.Collections)
	}
	if req.Text != "" || req.Match != "" || req.Temporality != "" || req.Since != "" {
		t.Errorf("query filters should be empty, got text=%q match=%q temporality=%q since=%q",
			req.Text, req.Match, req.Temporality, req.Since)
	}
	if len(req.Keywords) != 0 {
		t.Errorf("Keywords = %v, want empty", req.Keywords)
	}
	if len(req.Meta) != 0 {
		t.Errorf("Meta = %v, want empty", req.Meta)
	}
	if req.ReadOnly || req.HeadsOnly || req.DryRun {
		t.Errorf("options should default false, got read_only=%v heads_only=%v dry_run=%v",
			req.ReadOnly, req.HeadsOnly, req.DryRun)
	}
}
