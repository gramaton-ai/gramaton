package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	searchConfMin    float64
	searchConfMax    float64
	searchTemp       string
	searchKnowType  string
	searchEpStatus   string
	searchTop        int
	searchHistorical bool
	searchSince      string
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search the knowledge store",
	Long: `Tier 1 retrieval: discover relevant knowledge records.

Returns lightweight results with keywords, short summary, metadata
summary, confidence, and effective score. Use 'gramaton inspect <id>'
for full content.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().Float64Var(&searchConfMin, "confidence-min", 0, "minimum confidence filter")
	searchCmd.Flags().Float64Var(&searchConfMax, "confidence-max", 0, "maximum confidence filter")
	searchCmd.Flags().StringVar(&searchTemp, "temporality", "", "filter by temporality (immutable|durable|temporal|ephemeral)")
	searchCmd.Flags().StringVar(&searchKnowType, "knowledge-type", "", "filter by knowledge type")
	searchCmd.Flags().StringVar(&searchEpStatus, "epistemic-status", "", "filter by epistemic status")
	searchCmd.Flags().IntVar(&searchTop, "top", 10, "number of results")
	searchCmd.Flags().BoolVar(&searchHistorical, "include-historical", false, "include records with valid_until in the past")
	searchCmd.Flags().StringVar(&searchSince, "since", "", "filter: created after this date (YYYY-MM-DD or RFC3339)")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"top":               searchTop,
		"include_historical": searchHistorical,
	}

	if len(args) > 0 {
		body["text"] = args[0]
	}
	if searchTemp != "" {
		body["temporality"] = searchTemp
	}
	if searchKnowType != "" {
		body["knowledge_type"] = searchKnowType
	}
	if searchEpStatus != "" {
		body["epistemic_status"] = searchEpStatus
	}
	if cmd.Flags().Changed("confidence-min") {
		body["confidence_min"] = searchConfMin
	}
	if cmd.Flags().Changed("confidence-max") {
		body["confidence_max"] = searchConfMax
	}
	if searchSince != "" {
		body["since"] = searchSince
	}

	resp, err := serverPost("/v1/search", body)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	return printEnvelope(resp)
}

