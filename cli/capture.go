package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var captureFile string

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Store a knowledge record",
	Long: `Reads a JSON object from stdin or --file containing the record content
and optional metadata. Creates a node in the knowledge graph, generates
embeddings if a provider is configured, and commits the change.

Required fields:
  content    The knowledge to store (string)

Optional fields:
  temporality, confidence, knowledge_type, epistemic_status,
  importance, keywords, summary_short, summary_medium,
  source_ref, source_credibility, testimony_hops,
  context_about, context_who, context_prompted,
  context_findable_by, context_related,
  valid_from, valid_until`,
	RunE: runCapture,
}

func init() {
	captureCmd.Flags().StringVarP(&captureFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(captureCmd)
}

func runCapture(cmd *cobra.Command, args []string) error {
	// Read the input (from file or stdin) using the existing v0.1 reader.
	// The server accepts the same JSON shape via POST /v1/records.
	var input map[string]any
	if captureFile != "" {
		if err := readInputJSON(captureFile, &input, defaultLimits()); err != nil {
			return fmt.Errorf("input_error: %s", err)
		}
	} else {
		if err := readStdinJSON(&input, defaultLimits()); err != nil {
			return fmt.Errorf("input_error: %s", err)
		}
	}

	resp, err := serverPost("/v1/records", input)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}

	return printEnvelope(resp)
}

