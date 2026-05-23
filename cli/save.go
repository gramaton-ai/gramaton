package cli

import (
	"github.com/spf13/cobra"
)

var saveFile string

var saveCmd = &cobra.Command{
	Use:   "save",
	Short: "Store a knowledge record",
	Long: `Reads a JSON object from stdin or --file containing the record content
and optional metadata. Creates a record in Memory, generates
embeddings if a provider is configured, and commits the change.

Required fields:
  content    The knowledge to store (string)

Optional fields:
  temporality, confidence, knowledge_type, epistemic_status,
  importance, keywords, summary_short, source_ref,
  context_about, context_who, context_findable_by,
  context_source_type, context_time_sensitivity,
  context_reliability, context_capture_reason,
  asserted_as_of, meta, valid_from, valid_until`,
	RunE: runSave,
}

func init() {
	saveCmd.Flags().StringVarP(&saveFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(saveCmd)
}

func runSave(cmd *cobra.Command, args []string) error {
	// The server accepts the same JSON shape via POST /v1/records.
	input, err := readCommandInput(saveFile)
	if err != nil {
		return err
	}

	resp, err := serverPost("/v1/records", input)
	if err != nil {
		return writeServerError("save", err)
	}

	return printEnvelope(resp)
}

