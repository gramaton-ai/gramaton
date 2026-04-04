package cli

import (
	"fmt"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var updateFile string

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a record or create an edge",
	Long: `Reads a JSON object from stdin. Two modes:

Property update: set id and any metadata fields to change.
  {"id": "...", "confidence": 0.4, "epistemic_status": "contested"}

Edge creation: set id, link_to, edge_type, and optionally edge_weight.
  {"id": "...", "link_to": "...", "edge_type": "justifies", "edge_weight": 0.9}`,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().StringVarP(&updateFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(updateCmd)
}

type updateInput struct {
	ID              string   `json:"id"`
	Confidence      *float64 `json:"confidence,omitempty"`
	Temporality     string   `json:"temporality,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Importance      *float64 `json:"importance,omitempty"`

	LinkTo     string   `json:"link_to,omitempty"`
	EdgeType   string   `json:"edge_type,omitempty"`
	EdgeWeight *float64 `json:"edge_weight,omitempty"`
}

type updateOutput struct {
	ID      string `json:"id"`
	Updated bool   `json:"updated"`
	EdgeID  string `json:"edge_id,omitempty"`
}

func runUpdate(cmd *cobra.Command, args []string) error {
	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	var input updateInput
	if err := readInputJSON(updateFile, &input, eng.cfg.Limits); err != nil {
		return writeError("input_error", err.Error(), true)
	}

	if input.ID == "" {
		return writeError("missing_field", "id is required", true)
	}

	// Validate fields.
	if err := validateFloat64Range("confidence", input.Confidence, 0.0, 1.0); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}
	if err := validateFloat64Range("importance", input.Importance, 0.0, 1.0); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}
	if err := validateFloat64Range("edge_weight", input.EdgeWeight, 0.0, 1.0); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}
	if err := validateEnum("temporality", input.Temporality, validTemporalities); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}
	if err := validateEnum("knowledge_type", input.KnowledgeType, validKnowledgeTypes); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}
	if err := validateEnum("epistemic_status", input.EpistemicStatus, validEpistemicStatuses); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}

	if _, ok := eng.graph.GetNode(input.ID); !ok {
		return writeError("not_found", fmt.Sprintf("record %s not found", input.ID), false)
	}

	out := updateOutput{ID: input.ID}

	if input.LinkTo != "" {
		if input.EdgeType == "" {
			return writeError("missing_field", "edge_type is required when link_to is set", true)
		}
		weight := 0.5
		if input.EdgeWeight != nil {
			weight = *input.EdgeWeight
		}
		e, err := eng.graph.AddEdge(input.ID, input.LinkTo, input.EdgeType, weight, nil)
		if err != nil {
			return writeError("edge_error", err.Error(), false)
		}
		out.EdgeID = e.ID
		out.Updated = true
	}

	if input.Confidence != nil {
		setProp(eng, input.ID, "confidence", graph.Float64Property(*input.Confidence))
		out.Updated = true
	}
	if input.Temporality != "" {
		setProp(eng, input.ID, "temporality", graph.StringProperty(input.Temporality))
		out.Updated = true
	}
	if input.KnowledgeType != "" {
		setProp(eng, input.ID, "knowledge_type", graph.StringProperty(input.KnowledgeType))
		out.Updated = true
	}
	if input.EpistemicStatus != "" {
		setProp(eng, input.ID, "epistemic_status", graph.StringProperty(input.EpistemicStatus))
		out.Updated = true
	}
	if input.Importance != nil {
		setProp(eng, input.ID, "importance", graph.Float64Property(*input.Importance))
		out.Updated = true
	}

	if out.Updated {
		if _, err := eng.save("update"); err != nil {
			return writeError("save_error", err.Error(), false)
		}
	}

	return printJSON(out)
}

func setProp(eng *engine, nodeID, key string, val graph.Property) {
	if n, ok := eng.graph.GetNode(nodeID); ok {
		if old, ok := n.Properties[key]; ok {
			eng.propIdx.Remove(nodeID, key, old)
		}
	}
	eng.graph.SetNodeProperty(nodeID, key, val)
	eng.propIdx.Add(nodeID, key, val)
}
