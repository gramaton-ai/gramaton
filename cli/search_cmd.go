package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	searchConfMin      float64
	searchConfMax      float64
	searchImpMin       float64
	searchImpMax       float64
	searchTemp         string
	searchKnowType     string
	searchEpStatus     string
	searchTop          int
	searchHistorical   bool
	searchSince        string
	searchSort         string
	searchOrder        string
	searchMatch        string
	searchSimilarTo    string
	searchKeywords     string
	searchMissing      string
	searchRandom       bool
	searchAccessMin    int64
	searchAccessMax    int64
	searchLastAfter    string
	searchLastBefore   string
	searchValidAfter   string
	searchValidBefore  string
	searchExpAfter     string
	searchExpBefore    string
	searchMinEdges     int
	searchMaxEdges     int
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
	f := searchCmd.Flags()
	f.Float64Var(&searchConfMin, "confidence-min", 0, "minimum confidence (0-1)")
	f.Float64Var(&searchConfMax, "confidence-max", 0, "maximum confidence (0-1)")
	f.Float64Var(&searchImpMin, "importance-min", 0, "minimum importance (0-1)")
	f.Float64Var(&searchImpMax, "importance-max", 0, "maximum importance (0-1)")
	f.StringVar(&searchTemp, "temporality", "", "filter: immutable|durable|temporal|ephemeral (prefix ! to exclude)")
	f.StringVar(&searchKnowType, "knowledge-type", "", "filter: episodic|semantic|procedural|conceptual|reference")
	f.StringVar(&searchEpStatus, "epistemic-status", "", "filter: well_established|probable|speculative|contested|refuted")
	f.IntVar(&searchTop, "top", 10, "number of results (max 1000)")
	f.BoolVar(&searchHistorical, "include-historical", false, "include records past valid_until")
	f.StringVar(&searchSince, "since", "", "created after date (YYYY-MM-DD)")
	f.StringVar(&searchSort, "sort", "", "sort by: created_at|last_accessed|access_count|confidence|importance|content_length|edge_count|staleness")
	f.StringVar(&searchOrder, "order", "", "asc or desc (default: desc)")
	f.StringVar(&searchMatch, "match", "", "literal substring match (case-insensitive)")
	f.StringVar(&searchSimilarTo, "similar-to", "", "record ID to find similar records")
	f.StringVar(&searchKeywords, "keywords", "", "comma-separated keywords (exact match, all required)")
	f.StringVar(&searchMissing, "missing", "", "comma-separated field names that must be unset")
	f.BoolVar(&searchRandom, "random", false, "return random results")
	f.Int64Var(&searchAccessMin, "access-count-min", 0, "minimum access count")
	f.Int64Var(&searchAccessMax, "access-count-max", 0, "maximum access count")
	f.StringVar(&searchLastAfter, "last-accessed-after", "", "accessed after date")
	f.StringVar(&searchLastBefore, "last-accessed-before", "", "accessed before date")
	f.StringVar(&searchValidAfter, "valid-after", "", "valid_from after date")
	f.StringVar(&searchValidBefore, "valid-before", "", "valid_from before date")
	f.StringVar(&searchExpAfter, "expires-after", "", "valid_until after date")
	f.StringVar(&searchExpBefore, "expires-before", "", "valid_until before date")
	f.IntVar(&searchMinEdges, "min-edges", -1, "minimum edge count")
	f.IntVar(&searchMaxEdges, "max-edges", -1, "maximum edge count (0 = orphans)")
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
	if cmd.Flags().Changed("importance-min") {
		body["importance_min"] = searchImpMin
	}
	if cmd.Flags().Changed("importance-max") {
		body["importance_max"] = searchImpMax
	}
	if searchSince != "" {
		body["since"] = searchSince
	}
	if searchSort != "" {
		body["sort"] = searchSort
	}
	if searchOrder != "" {
		body["order"] = searchOrder
	}
	if searchMatch != "" {
		body["match"] = searchMatch
	}
	if searchSimilarTo != "" {
		body["similar_to"] = searchSimilarTo
	}
	if searchKeywords != "" {
		body["keywords"] = strings.Split(searchKeywords, ",")
	}
	if searchMissing != "" {
		body["missing"] = strings.Split(searchMissing, ",")
	}
	if searchRandom {
		body["random"] = true
	}
	if cmd.Flags().Changed("access-count-min") {
		body["access_count_min"] = searchAccessMin
	}
	if cmd.Flags().Changed("access-count-max") {
		body["access_count_max"] = searchAccessMax
	}
	if searchLastAfter != "" {
		body["last_accessed_after"] = searchLastAfter
	}
	if searchLastBefore != "" {
		body["last_accessed_before"] = searchLastBefore
	}
	if searchValidAfter != "" {
		body["valid_after"] = searchValidAfter
	}
	if searchValidBefore != "" {
		body["valid_before"] = searchValidBefore
	}
	if searchExpAfter != "" {
		body["expires_after"] = searchExpAfter
	}
	if searchExpBefore != "" {
		body["expires_before"] = searchExpBefore
	}
	if searchMinEdges >= 0 {
		body["min_edges"] = searchMinEdges
	}
	if searchMaxEdges >= 0 {
		body["max_edges"] = searchMaxEdges
	}

	resp, err := serverPost("/v1/search", body)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	return printEnvelope(resp)
}

