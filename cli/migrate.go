package cli

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Upgrade the store to the current format version",
	Long: `Upgrade the on-disk store to the current format version.

Required one-time action when a gramaton binary upgrade bumps the
store format. Currently upgrades v1 stores to v2 (D7 timestamp-
indexed commits, needed for date-bounded temporal queries).

The command is idempotent: a rerun against an already-current store
is a no-op, and a rerun after a partial crash resumes cleanly
(backfill Puts are idempotent in bbolt).

Refuses to run while a gramaton server is active to prevent
concurrent writes. Stop the server first: gramaton stop.`,
	RunE: runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}

func runMigrate(cmd *cobra.Command, args []string) error {
	dir := configDir()

	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
	}

	if err := core.MigrateStore(dir, []string{baseConfigDir()}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	fmt.Println("Migration complete.")
	return nil
}
