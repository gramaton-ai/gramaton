package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var classifyCmd = &cobra.Command{
	Use:   "classify",
	Short: "Classify a pending record",
	Long: `Reads a JSON object from stdin with the record ID and classification
metadata. Updates the record's properties and sets processing_status
to "processed".

Example:
  {"id": "...", "temporality": "durable", "confidence": 0.9,
   "knowledge_type": "episodic", "keywords": ["kafka"]}`,
	RunE: runClassify,
}

func init() {
	rootCmd.AddCommand(classifyCmd)
}

type classifyInput struct {
	ID              string   `json:"id"`
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Importance      *float64 `json:"importance,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
	SummaryAbstract string   `json:"summary_abstract,omitempty"`
}

func runClassify(cmd *cobra.Command, args []string) error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return writeError("stdin_error", "Failed to read stdin", true)
	}
	if len(data) == 0 {
		return writeError("empty_input", "No input received on stdin", true)
	}

	var input classifyInput
	if err := json.Unmarshal(data, &input); err != nil {
		return writeError("malformed_json", fmt.Sprintf("JSON parse error: %s", err), true)
	}
	if input.ID == "" {
		return writeError("missing_field", "id is required", true)
	}

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	if _, ok := eng.graph.GetNode(input.ID); !ok {
		return writeError("not_found", fmt.Sprintf("record %s not found", input.ID), false)
	}

	// Apply classification fields.
	if input.Temporality != "" {
		setProp(eng, input.ID, "temporality", graph.StringProperty(input.Temporality))
	}
	if input.Confidence != nil {
		setProp(eng, input.ID, "confidence", graph.Float64Property(*input.Confidence))
	}
	if input.KnowledgeType != "" {
		setProp(eng, input.ID, "knowledge_type", graph.StringProperty(input.KnowledgeType))
	}
	if input.EpistemicStatus != "" {
		setProp(eng, input.ID, "epistemic_status", graph.StringProperty(input.EpistemicStatus))
	}
	if input.Importance != nil {
		setProp(eng, input.ID, "importance", graph.Float64Property(*input.Importance))
	}
	if len(input.Keywords) > 0 {
		setProp(eng, input.ID, "content_keywords", graph.StringListProperty(input.Keywords))
	}
	if input.SummaryShort != "" {
		setProp(eng, input.ID, "content_short", graph.StringProperty(input.SummaryShort))
	}
	if input.SummaryAbstract != "" {
		setProp(eng, input.ID, "content_abstract", graph.StringProperty(input.SummaryAbstract))
	}

	// Mark as processed.
	setProp(eng, input.ID, "processing_status", graph.StringProperty("processed"))

	if _, err := eng.save("classify"); err != nil {
		return writeError("save_error", err.Error(), false)
	}

	return printJSON(updateOutput{ID: input.ID, Updated: true})
}
