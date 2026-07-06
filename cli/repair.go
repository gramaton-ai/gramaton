package cli

import (
	"fmt"
	"log/slog"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var (
	repairDryRun         bool
	repairContentQuality bool
)

var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Fix store integrity issues",
	Long: `Repairs structural issues in the knowledge store: removes dangling
edges and orphaned chunk nodes. Reports stale embeddings (fix with
'gramaton reembed').

With --content-quality, also scans every record for LLM tool-use-
format contamination (e.g. summary_short containing stray
</summary_short> or <parameter name=> tags from agent output drift)
and applies a deterministic repair cascade: strip the bad tail,
fall back to extracting the first sentences of content_full, or
flag the record for a future LLM-escalation pass.

Run 'gramaton validate' first to see what will be fixed.
Use --dry-run to preview changes without applying them.`,
	RunE: runRepair,
}

func init() {
	repairCmd.Flags().BoolVar(&repairDryRun, "dry-run", false, "preview changes without applying")
	repairCmd.Flags().BoolVar(&repairContentQuality, "content-quality", false,
		"also run the content-quality self-heal pass (detects and repairs LLM tool-use-format contamination in summary_short)")
	rootCmd.AddCommand(repairCmd)
}

func runRepair(cmd *cobra.Command, args []string) error {
	if err := guardLocalStore("repair"); err != nil {
		return err
	}
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
	// Release the engine's file handles (and the bbolt flock) on
	// every exit path -- the read-only refusal below returns early,
	// and in-process callers (tests) reopen the same store right
	// after.
	defer func() { _ = eng.Close() }()

	// Read-only gate for every mutating path of this command: Repair
	// and the --content-quality self-heal both write DURABLY before
	// Save (Repair's DeleteEdge persists straight to the bbolt edge
	// store; self-heal's SetProp to the property index), so the
	// engine's Save backstop alone would reject the commit AFTER the
	// mutation already stuck -- the worst of both worlds on a frozen
	// artifact. Refuse up front instead, same message shape as the
	// api guards. The --dry-run path stays allowed: it only runs
	// Validate, like `gramaton validate`.
	if !repairDryRun && eng.ReadOnly() {
		return fmt.Errorf("store is read-only: repair is not permitted (make it writable first: gramaton store thaw)")
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

	if repairContentQuality {
		fmt.Println()
		fmt.Println("Running content-quality self-heal...")
		healResult := curation.RunSelfHeal(eng, slog.Default())
		fmt.Printf("  Scanned: %d\n", healResult.Scanned)
		fmt.Printf("  Repaired: %d\n", healResult.Repaired)
		if healResult.FlaggedForLLM > 0 {
			fmt.Printf("  Flagged for LLM-escalation repair: %d\n", healResult.FlaggedForLLM)
			fmt.Println("  (Records whose summaries couldn't be salvaged by strip or content_full fallback.")
			fmt.Println("  Query them later with gramaton_search meta.repair_needed_llm=true once an")
			fmt.Println("  LLM-escalation pass is available.)")
		}
	}

	return nil
}
