package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var classifyFile string

var classifyCmd = &cobra.Command{
	Use:   "classify",
	Short: "Classify a pending record",
	Long: `Reads a JSON object from stdin or --file with the record ID and
classification metadata. Updates the record's properties and sets
processing_status to "processed".

Example:
  {"id": "...", "temporality": "durable", "confidence": 0.9,
   "knowledge_type": "episodic", "keywords": ["kafka"]}`,
	RunE: runClassify,
}

func init() {
	classifyCmd.Flags().StringVarP(&classifyFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(classifyCmd)
}

func runClassify(cmd *cobra.Command, args []string) error {
	var input map[string]any
	if classifyFile != "" {
		if err := readInputJSON(classifyFile, &input, defaultLimits()); err != nil {
			return writeError("input_error", err.Error(), true)
		}
	} else {
		if err := readStdinJSON(&input, defaultLimits()); err != nil {
			return writeError("input_error", err.Error(), true)
		}
	}

	id, _ := input["id"].(string)
	if id == "" {
		return writeError("missing_field", "id is required", true)
	}

	// Remove id from body -- it goes in the URL.
	delete(input, "id")

	resp, err := serverPost(fmt.Sprintf("/v1/records/%s/classify", id), input)
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}

	return printEnvelope(resp)
}
