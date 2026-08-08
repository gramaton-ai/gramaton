package core

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// MigrateStore brings a store up to the current StoreFormatVersion.
// Called from the `gramaton migrate` CLI. Idempotent: safe to rerun.
//
// Currently-supported upgrade path:
//
//	v0 -> v2   (fresh store that had no FORMAT file; no backfill needed)
//	v1 -> v2   (D7 timestamp index backfill)
//	v2 -> v2   (no-op)
//
// Opens the engine via the migration-private skipFormatCheck path;
// the normal LoadEngine boot gate refuses v1 stores and this is the
// only codepath that bypasses it. The FORMAT file is bumped only
// after the backfill completes: a crash before the bump leaves
// FORMAT at the older version and a rerun re-migrates cleanly (the
// backfill's index writes are idempotent).
//
// Collection-level defaults (clear_mode, curation) are intentionally
// NOT set here. Those fields arrive in Phase 4 of the temporal-
// queries build with read-time fallbacks; an explicit sweep costs
// complexity for no behavioral gain. If a future phase wants
// populated defaults for visibility, that phase owns its own sweep.
func MigrateStore(cfgDir string, globalCfgDirs []string) error {
	eng, err := loadEngineWithOptions(cfgDir, globalCfgDirs, nil, true)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer eng.Close()

	dataDir := eng.cfg.DataDir

	current, err := ReadFormatVersion(dataDir)
	if err != nil {
		return fmt.Errorf("read FORMAT: %w", err)
	}

	if current == version.StoreFormatVersion {
		slog.Debug("store already at current version; nothing to do",
			"component", "migrate",
			"version", current)
		return nil
	}
	if current > version.StoreFormatVersion {
		return fmt.Errorf(
			"store is at v%d; this binary supports up to v%d. Upgrade gramaton.",
			current, version.StoreFormatVersion,
		)
	}

	slog.Info("migrating store",
		"component", "migrate",
		"from", current,
		"to", version.StoreFormatVersion)

	if err := backfillTSIndex(eng); err != nil {
		return fmt.Errorf("backfill timestamp index: %w", err)
	}

	if err := WriteFormatVersion(dataDir); err != nil {
		return fmt.Errorf("write FORMAT: %w", err)
	}

	slog.Info("migration complete",
		"component", "migrate",
		"version", version.StoreFormatVersion)
	return nil
}

// backfillTSIndex walks the commit chain from HEAD to root, writing
// one timestamp-index entry per commit. Bbolt Put is idempotent on
// identical (key, value) pairs, so reruns after a partial failure
// redo at most the tail of already-indexed commits without corrupting
// state.
func backfillTSIndex(eng *Engine) error {
	start := time.Now()
	head := eng.HeadHash()
	if head == "" {
		slog.Debug("no HEAD commit; timestamp index is empty",
			"component", "migrate")
		return nil
	}
	floor := eng.HistoryFloor()
	count := 0
	for hash := head; hash != ""; {
		c, err := graph.LoadCommitMeta(eng.store, hash)
		if err != nil {
			return fmt.Errorf("load commit %s: %w", hash, err)
		}
		if err := eng.indexes.tsIndex.Put(c); err != nil {
			return fmt.Errorf("index commit %s: %w", hash, err)
		}
		count++
		if count%500 == 0 {
			slog.Info("backfilling timestamp index",
				"component", "migrate",
				"commits_processed", count)
		}
		// A pruned chain ends at the oldest kept commit, whose Parent
		// still names the pruned commit by hash; following it would
		// fail on a deliberately absent chunk.
		if floor != nil && hash == floor.OldestKeptCommit {
			slog.Info("timestamp index backfill grounded at the prune floor",
				"component", "migrate", "commits", count)
			break
		}
		hash = c.Parent
	}
	slog.Info("timestamp index backfill complete",
		"component", "migrate",
		"commits", count,
		"elapsed", time.Since(start).Round(time.Millisecond).String())
	return nil
}
