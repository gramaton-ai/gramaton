package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create a backup of the knowledge store",
	Long: `Writes a compressed .tar.gz snapshot of the knowledge store to the
server-configured backup directory (backup.dir; default
~/.gramaton/backups). Takes no arguments: the destination is always
that directory, never a caller-supplied path. Filenames are
timestamped, so snapshots accumulate; backup.retain bounds how many
are kept and prunes the oldest.

The archive captures the committed store -- the content-addressed
chunks plus a coherent snapshot of HEAD, refs, FORMAT, and jobs.db --
and a sanitized copy of config.yaml with API keys and endpoints
stripped. Derived indexes (rebuilt on restore) and transient files
(server.json, logs, in-flight temp files) are left out. HEAD, refs,
and FORMAT are read under a brief read lock and the compression runs
off-lock, so the archive is one consistent moment and concurrent
writes are not blocked.

Restore an archive with: gramaton restore <file>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := serverPostSlow("/v1/backup", map[string]any{})
		if err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)
}
