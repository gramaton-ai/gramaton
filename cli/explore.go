package cli

import (
	"fmt"
	"strings"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var (
	exploreDepth     int
	exploreEdgeTypes string
	exploreMinWeight float64
)

var exploreCmd = &cobra.Command{
	Use:   "explore <record-id>",
	Short: "Explore the knowledge graph",
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
	nodeID := args[0]

	eng, err := loadEngine()
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	if _, ok := eng.graph.GetNode(nodeID); !ok {
		return writeError("not_found", fmt.Sprintf("record %s not found", nodeID), false)
	}

	opts := graph.TraverseOptions{
		MaxDepth:      exploreDepth,
		MinEdgeWeight: exploreMinWeight,
	}
	if exploreEdgeTypes != "" {
		opts.EdgeTypes = strings.Split(exploreEdgeTypes, ",")
	}

	sub := eng.graph.Traverse(nodeID, opts)
	return printJSON(sub)
}
