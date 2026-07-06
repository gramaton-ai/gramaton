package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show aggregate statistics for the knowledge store",
	Long: `Reports the metadata composition of the active store: the total record
count, plus distributions of records by temporality, knowledge_type, and
epistemic_status, and a confidence breakdown into bands (high >= 0.9,
medium 0.7-0.9, moderate 0.4-0.7, low < 0.4, and unset). A record is
tallied under a field only when it has that field set, so a distribution
may sum to less than the total.

The total counts every non-deleted record, including derived concept
nodes; structural chunk nodes and deleted records are excluded. Read-only
-- it takes a read lock and never writes, so it is safe against a live or
frozen store. For node and edge counts and embedding health, see
'gramaton status'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := serverGet("/v1/stats")
		if err != nil {
			return fmt.Errorf("stats: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	rootCmd.AddCommand(statsCmd)
}
