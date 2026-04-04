package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/search"
	"github.com/spf13/cobra"
)

var (
	searchConfMin    float64
	searchConfMax    float64
	searchTemp       string
	searchKnowType   string
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
	eng, err := loadEngine()
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	q := search.Query{
		Top:               searchTop,
		Temporality:       searchTemp,
		KnowledgeType:     searchKnowType,
		EpistemicStatus:   searchEpStatus,
		IncludeHistorical: searchHistorical,
	}

	if len(args) > 0 {
		q.Text = args[0]
	}

	if cmd.Flags().Changed("confidence-min") {
		q.ConfidenceMin = &searchConfMin
	}
	if cmd.Flags().Changed("confidence-max") {
		q.ConfidenceMax = &searchConfMax
	}
	if searchSince != "" {
		t, err := parseDateArg(searchSince)
		if err != nil {
			return fmt.Errorf("parse --since: %w", err)
		}
		q.Since = &t
	}

	results, err := eng.searcher.Execute(context.Background(), q)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	// Record access and spread activation for returned results.
	if len(results) > 0 {
		now := time.Now().UTC()
		activationCfg := graph.ActivationConfig{
			BaseAmount:        eng.cfg.Activation.BaseAmount,
			AttenuationFactor: eng.cfg.Activation.AttenuationFactor,
		}
		for _, r := range results {
			eng.graph.RecordAccess(r.ID, now, activationCfg)
		}
		if _, err := eng.save("access"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save access data: %s\n", err)
		}
	}

	return printJSON(results)
}
