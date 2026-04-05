package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search the knowledge store",
	Long: `Tier 1 retrieval: discover relevant knowledge records.

Query text is optional -- omit it for filter-only queries.
Returns lightweight results with keywords, short summary, metadata
summary, confidence, and effective score. Use 'gramaton inspect <id>'
for full content.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSearch,
}

func init() {
	addSearchFlags(searchCmd)
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	body := buildSearchBody(cmd, args)

	resp, err := serverPost("/v1/search", body)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	return printEnvelope(resp)
}
