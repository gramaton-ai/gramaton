package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var reembedBatch int

var reembedCmd = &cobra.Command{
	Use:   "reembed",
	Short: "Re-embed records with stale embeddings",
	Long: `Finds records where embedding_model differs from the current
provider's model and regenerates their embeddings. Use after
changing embedding models or providers.`,
	RunE: runReembed,
}

func init() {
	reembedCmd.Flags().IntVar(&reembedBatch, "batch", 0, "maximum number of records to process (0 = all)")
	rootCmd.AddCommand(reembedCmd)
}

func runReembed(cmd *cobra.Command, args []string) error {
	resp, err := serverPost("/v1/reembed", map[string]int{"batch": reembedBatch})
	if err != nil {
		return writeError("reembed_error", fmt.Sprintf("reembed: %s", err), false)
	}

	return printEnvelope(resp)
}
