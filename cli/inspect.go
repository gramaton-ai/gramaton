package cli

import (
	"fmt"
	"time"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <record-id>",
	Short: "Inspect a knowledge record",
	Long: `Tier 2 retrieval: view full content, all metadata, and related
records for a specific node.`,
	Args: cobra.ExactArgs(1),
	RunE: runInspect,
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}

// inspectOutput is the structured output for inspect.
type inspectOutput struct {
	ID              string          `json:"id"`
	Properties      map[string]any  `json:"properties"`
	MetadataSummary string          `json:"metadata_summary"`
	Related         []relatedOutput `json:"related,omitempty"`
}

type relatedOutput struct {
	ID           string  `json:"id"`
	EdgeType     string  `json:"edge_type"`
	EdgeWeight   float64 `json:"edge_weight"`
	Direction    string  `json:"direction"`
	SummaryShort string  `json:"summary_short,omitempty"`
}

func runInspect(cmd *cobra.Command, args []string) error {
	nodeID := args[0]

	eng, err := loadEngine()
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	n, ok := eng.graph.GetNode(nodeID)
	if !ok {
		return writeError("not_found", fmt.Sprintf("record %s not found", nodeID), false)
	}

	// Convert properties to a JSON-friendly format.
	props := make(map[string]any, len(n.Properties))
	for k, v := range n.Properties {
		props[k] = v.FormatValue()
	}

	out := inspectOutput{
		ID:              n.ID,
		Properties:      props,
		MetadataSummary: inspectMetadataSummary(n.Properties),
	}

	// Gather related nodes via edges.
	for _, e := range eng.graph.EdgesFrom(nodeID) {
		rel := relatedOutput{
			ID:         e.TargetID,
			EdgeType:   e.Type,
			EdgeWeight: e.Weight,
			Direction:  "outbound",
		}
		if target, ok := eng.graph.GetNode(e.TargetID); ok {
			if v, ok := target.Properties["content_short"]; ok {
				rel.SummaryShort = v.String()
			}
		}
		out.Related = append(out.Related, rel)
	}
	for _, e := range eng.graph.EdgesTo(nodeID) {
		rel := relatedOutput{
			ID:         e.SourceID,
			EdgeType:   e.Type,
			EdgeWeight: e.Weight,
			Direction:  "inbound",
		}
		if source, ok := eng.graph.GetNode(e.SourceID); ok {
			if v, ok := source.Properties["content_short"]; ok {
				rel.SummaryShort = v.String()
			}
		}
		out.Related = append(out.Related, rel)
	}

	return printJSON(out)
}

func inspectMetadataSummary(props graph.Properties) string {
	now := time.Now().UTC()
	var parts []string

	if vu, ok := props["valid_until"]; ok {
		if vu.Timestamp().Before(now) {
			parts = append(parts, "Historical.")
		} else {
			parts = append(parts, "Current.")
		}
	} else {
		parts = append(parts, "Current.")
	}

	if v, ok := props["temporality"]; ok {
		s := v.String()
		parts = append(parts, s[0:1]+s[1:]) // preserve case
	}
	if v, ok := props["confidence"]; ok {
		parts = append(parts, fmt.Sprintf("confidence %.2f", v.Float64()))
	}
	if v, ok := props["epistemic_status"]; ok {
		s := v.String()
		if s == "well_established" {
			s = "well-established"
		}
		parts = append(parts, s)
	}

	result := ""
	for i, p := range parts {
		if i == 0 {
			result = p
		} else if i == 1 {
			result += " " + p
		} else {
			result += ", " + p
		}
	}
	return result
}
