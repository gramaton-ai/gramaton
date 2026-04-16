package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	exploreDepth     int
	exploreEdgeTypes string
	exploreMinWeight float64
)

var exploreCmd = &cobra.Command{
	Use:   "explore <record-id>",
	Short: "Explore the graph",
	Long: `Tier 3 retrieval: traverse the graph from a starting node.

Returns a subgraph fragment -- connected nodes and edges within
the specified depth. Follows both inbound and outbound edges.`,
	Args: cobra.ExactArgs(1),
	RunE: runExplore,
}

func init() {
	exploreCmd.Flags().IntVar(&exploreDepth, "depth", 2, "maximum traversal depth")
	exploreCmd.Flags().StringVar(&exploreEdgeTypes, "edge-types", "", "comma-separated edge types to follow (default: all)")
	exploreCmd.Flags().Float64Var(&exploreMinWeight, "min-weight", 0, "minimum edge weight to follow")
	rootCmd.AddCommand(exploreCmd)
}

func runExplore(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"node_id": args[0],
		"depth":   exploreDepth,
	}
	if exploreEdgeTypes != "" {
		body["edge_types"] = strings.Split(exploreEdgeTypes, ",")
	}
	if exploreMinWeight > 0 {
		body["min_weight"] = exploreMinWeight
	}

	resp, err := serverPost("/v1/explore", body)
	if err != nil {
		return fmt.Errorf("explore: %w", err)
	}

	return printEnvelope(resp)
}
