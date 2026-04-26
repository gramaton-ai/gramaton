package cli

import (
	"fmt"
	"net/url"

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
	input, err := readCommandInput(classifyFile)
	if err != nil {
		return err
	}
	id, err := extractRequiredID(input)
	if err != nil {
		return err
	}

	resp, err := serverPost(fmt.Sprintf("/v1/records/%s/classify", url.PathEscape(id)), input)
	if err != nil {
		return writeServerError("classify", err)
	}

	return printEnvelope(resp)
}
