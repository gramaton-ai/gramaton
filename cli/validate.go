package cli

import (
	"fmt"
	"os"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check store integrity",
	Long: `Validates the knowledge store for structural issues: broken edges,
orphaned chunks, index consistency, embedding dimension mismatches,
and format version. Reports errors and warnings.

Exit code 0 if the store is healthy, 1 if errors are found.`,
	RunE: runValidate,
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(cmd *cobra.Command, args []string) error {
	dir := configDir()

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	eng.RLock()
	result := eng.Validate()
	eng.RUnlock()

	// Print results.
	fmt.Printf("Nodes: %d  Edges: %d  Collections: %d  Chunks: %d\n",
		result.Stats.Nodes, result.Stats.Edges,
		result.Stats.Collections, result.Stats.Chunks)
	fmt.Printf("BM25: %d docs  Vector: %d entries\n",
		result.Stats.BM25Docs, result.Stats.VecDocs)
	fmt.Println()

	if len(result.Warnings) > 0 {
		fmt.Printf("Warnings (%d):\n", len(result.Warnings))
		for _, w := range result.Warnings {
			fmt.Printf("  WARN  %s\n", w)
		}
		fmt.Println()
	}

	if len(result.Errors) > 0 {
		fmt.Printf("Errors (%d):\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("  ERROR %s\n", e)
		}
		fmt.Println()
		fmt.Println("Store has integrity issues.")
		os.Exit(1)
	}

	if len(result.Warnings) == 0 {
		fmt.Println("Store is healthy.")
	} else {
		fmt.Println("Store is healthy (with warnings).")
	}
	return nil
}
