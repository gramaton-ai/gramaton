package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	dupThreshold float64
	dupMaxPairs  int
)

var duplicatesCmd = &cobra.Command{
	Use:   "duplicates",
	Short: "Find near-duplicate records by embedding similarity",
	Long: `Scans the store for record pairs whose embeddings are at least --threshold
cosine-similar (default 0.92) and reports up to --max-pairs of them (default
50), ranked by similarity. A high score means two records likely carry the
same knowledge.

Read-only diagnostic: it surfaces consolidation candidates for you or
curation to merge, link, or supersede, and never writes anything itself.
Lower the threshold to widen the net (more, looser candidates); raise it to
narrow to near-identical pairs.

Examples:
  gramaton duplicates
  gramaton duplicates --threshold 0.85
  gramaton duplicates --threshold 0.97 --max-pairs 200`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{}
		if cmd.Flags().Changed("threshold") {
			body["threshold"] = dupThreshold
		}
		if cmd.Flags().Changed("max-pairs") {
			body["max_pairs"] = dupMaxPairs
		}

		resp, err := serverPost("/v1/duplicates", body)
		if err != nil {
			return fmt.Errorf("duplicates: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	duplicatesCmd.Flags().Float64Var(&dupThreshold, "threshold", 0.92, "minimum similarity (0-1)")
	duplicatesCmd.Flags().IntVar(&dupMaxPairs, "max-pairs", 50, "maximum pairs to return")
	rootCmd.AddCommand(duplicatesCmd)
}
