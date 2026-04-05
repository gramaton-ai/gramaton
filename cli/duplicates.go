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
	Args:  cobra.NoArgs,
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
