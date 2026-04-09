package cli

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var repairDryRun bool

var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Fix store integrity issues",
	Long: `Repairs structural issues in the knowledge store: removes dangling
edges and orphaned chunk nodes. Reports stale embeddings (fix with
'gramaton reembed').

Run 'gramaton validate' first to see what will be fixed.
Use --dry-run to preview changes without applying them.`,
	RunE: runRepair,
}

func init() {
	repairCmd.Flags().BoolVar(&repairDryRun, "dry-run", false, "preview changes without applying")
	rootCmd.AddCommand(repairCmd)
}

func runRepair(cmd *cobra.Command, args []string) error {
	dir := configDir()

	// Refuse to run while the server is active to prevent concurrent
	// writes to the same store.
	if !repairDryRun {
		if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
			return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
		}
	}

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	if repairDryRun {
		// Validate only -- show what would be repaired.
		eng.RLock()
		result := eng.Validate()
		eng.RUnlock()

		fmt.Println("Dry run -- no changes applied.")
		fmt.Println()

		dangling := 0
		orphans := 0
		stale := 0
		for _, e := range result.Errors {
			fmt.Printf("  would fix: %s\n", e)
			dangling++ // errors are structural issues repair would fix
		}
		for _, w := range result.Warnings {
			fmt.Printf("  info: %s\n", w)
			stale++
		}

		if dangling == 0 && orphans == 0 {
			fmt.Println("\nNothing to repair.")
		}
		return nil
	}

	// Actual repair.
	eng.Lock()
	result := eng.Repair()
	eng.Unlock()

	for _, msg := range result.Messages {
		fmt.Printf("  %s\n", msg)
	}

	if result.DanglingEdgesRemoved == 0 && result.OrphanChunksRemoved == 0 && result.StaleEmbeddings == 0 {
		fmt.Println("Nothing to repair. Store is clean.")
	} else if result.DanglingEdgesRemoved == 0 && result.OrphanChunksRemoved == 0 {
		fmt.Println("\nNo structural repairs needed.")
	} else {
		fmt.Println("\nRepairs applied and saved.")
	}

	return nil
}
