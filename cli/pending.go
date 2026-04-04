package cli

import (
	"fmt"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var pendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List unclassified records",
	Long:  `Shows records with processing_status "captured" that have not yet been classified by an agent.`,
	RunE:  runPending,
}

func init() {
	rootCmd.AddCommand(pendingCmd)
}

type pendingOutput struct {
	ID           string `json:"id"`
	SummaryShort string `json:"summary_short,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func runPending(cmd *cobra.Command, args []string) error {
	eng, err := loadEngine()
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	captured := eng.propIdx.Lookup("processing_status", graph.StringProperty("captured"))

	var results []pendingOutput
	for _, id := range captured {
		n, ok := eng.graph.GetNode(id)
		if !ok {
			continue
		}
		p := pendingOutput{ID: id}
		if v, ok := n.Properties["content_short"]; ok {
			p.SummaryShort = v.String()
		}
		if v, ok := n.Properties["created_at"]; ok {
			p.CreatedAt = v.FormatValue()
		}
		results = append(results, p)
	}

	return printJSON(results)
}
