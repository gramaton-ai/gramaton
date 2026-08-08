package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/testutil"
)

// newBackfillReadOnlyTestBase builds an isolated config dir for the
// frozen-store backfill gate tests, mirroring newFreezeTestBase
// (store_readonly_test.go) but also pinning Backup.Dir under the temp
// dir: TestBackfillCollapseRefusesReadOnlyBeforeBackup exercises the
// --apply path, which -- pre-fix -- reaches backup.Create before the
// refusal ever fires, and Backup.Dir left at its default would point
// that write at the operator's real ~/.gramaton/backups.
func newBackfillReadOnlyTestBase(t *testing.T) (base, dataDir string) {
	t.Helper()
	base = t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Author.Name = "Ada Lovelace"
	cfg.Author.Email = "ada@example.com"
	cfg.Backup.Dir = filepath.Join(base, "backups")
	if err := config.Save(cfg, filepath.Join(base, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldCfgDir := cfgDir
	t.Cleanup(func() { cfgDir = oldCfgDir })
	t.Setenv("GRAMATON_STORE", "")
	return base, cfg.DataDir
}

// TestBackfillChangelogRefusesReadOnlyStore pins the frozen-store
// gate: the changelog index lives in the sidecar bbolt file, which
// stays open on a frozen store for access bookkeeping, so nothing
// else stops this command's full backfill write from landing there.
// Before the fix there was no read-only check at all in this command.
func TestBackfillChangelogRefusesReadOnlyStore(t *testing.T) {
	base, dataDir := newBackfillReadOnlyTestBase(t)

	eng, err := core.LoadEngine(base)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	testutil.Record("seed record for changelog backfill").AddDirect(eng)
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	cfgDir = base
	err = runBackfillChangelog(backfillChangelogCmd, nil)
	if err == nil {
		t.Fatal("backfill changelog should refuse on a frozen store")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %q, want a read-only refusal", err)
	}
}

// TestBackfillCollapseRefusesReadOnlyBeforeBackup pins both the gate
// AND its position: --apply on a frozen store must refuse before the
// backup + archive work, not after. A victim/successor pair is
// required for the refusal to be reachable at all -- with nothing to
// collapse, the command returns earlier ("Nothing to collapse") and
// never gets far enough to exercise this gate.
func TestBackfillCollapseRefusesReadOnlyBeforeBackup(t *testing.T) {
	base, dataDir := newBackfillReadOnlyTestBase(t)

	eng, err := core.LoadEngine(base)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	successor := testutil.Record("successor content").AddDirect(eng)
	victim := testutil.Record("victim content").
		Resolution("superseded").
		ValidUntil(time.Now().UTC().Add(-time.Hour)).
		AddDirect(eng)
	testutil.EdgeDirect(eng, successor, victim, "supersedes", 1.0)
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	cfgDir = base
	origApply, origMinWeight := backfillCollapseApply, backfillCollapseMinWeight
	backfillCollapseApply = true
	backfillCollapseMinWeight = 0.92
	t.Cleanup(func() {
		backfillCollapseApply, backfillCollapseMinWeight = origApply, origMinWeight
	})

	err = runBackfillCollapse(backfillCollapseCmd, nil)
	if err == nil {
		t.Fatal("backfill collapse --apply should refuse on a frozen store")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %q, want a read-only refusal", err)
	}

	// The refusal must fire BEFORE the backup: no archive written
	// under the configured (temp, harmless either way) backup dir.
	entries, statErr := os.ReadDir(filepath.Join(base, "backups"))
	if statErr == nil && len(entries) != 0 {
		t.Errorf("backup dir has %d entr(y/ies), want none -- refusal ran after the backup", len(entries))
	}
}
