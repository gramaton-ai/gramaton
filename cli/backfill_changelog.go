package cli

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var backfillChangelogCmd = &cobra.Command{
	Use:   "changelog",
	Short: "Index historical logical versions into the changelog",
	Long: `Walks the entire commit chain and indexes every record's logical
versions into the per-record changelog, so history tools cover
commits from before the changelog existed.

Version identity is content-based: commits that only persisted the
retired access bookkeeping (the periodic access_flush commits) are
skipped by label, and every remaining candidate change is verified
against the record's adjacent blobs with bookkeeping fields masked.
On stores with heavy read history this is the difference between
honest version counts and 10-40x phantom inflation.

Idempotent: re-running skips already-indexed entries, and an
interrupted run resumes where it stopped. Expect minutes up to
around an hour on a million-commit store; progress prints as it
goes.

Refuses to run while a gramaton server is active (the store is
locked). Stop the server first: gramaton stop.`,
	Args: cobra.NoArgs,
	RunE: runBackfillChangelog,
}

func init() {
	backfillCmd.AddCommand(backfillChangelogCmd)
}

func runBackfillChangelog(cmd *cobra.Command, args []string) error {
	if err := guardLocalStore("backfill"); err != nil {
		return err
	}
	dir := configDir()

	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
	}

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}
	defer eng.Close()

	// The changelog index lives in the sidecar bbolt file, which stays
	// open (for access bookkeeping) even on a frozen store -- so
	// nothing else stops this backfill from writing to it. Refuse up
	// front, same message shape as the other store-mutating commands.
	if eng.ReadOnly() {
		return fmt.Errorf("store is read-only: backfill changelog is not permitted (make it writable first: gramaton store thaw)")
	}

	fmt.Println("Walking the commit chain (progress prints every batch)...")
	indexed, err := eng.BackfillChangelog(func(done, total int) {
		fmt.Printf("  %d / %d commits\n", done, total)
	})
	if err != nil {
		return fmt.Errorf("changelog backfill: %w", err)
	}
	fmt.Printf("\nIndexed %d logical version(s). Changelog coverage now spans the full chain.\n", indexed)
	return nil
}
