package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var resolveFile string

var resolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Mark a record as resolved",
	Long: `Reads a JSON object from stdin or --file with the record ID and
resolution status. Sets resolution, resolved_at, and valid_until.

Example:
  {"id": "...", "resolution": "completed", "resolution_note": "shipped in v0.4"}

Valid resolution values: completed, superseded, abandoned, obsolete`,
	RunE: runResolve,
}

func init() {
	resolveCmd.Flags().StringVarP(&resolveFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(resolveCmd)
}

func runResolve(cmd *cobra.Command, args []string) error {
	var input map[string]any
	if resolveFile != "" {
		if err := readInputJSON(resolveFile, &input, defaultLimits()); err != nil {
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

	resp, err := serverPost(fmt.Sprintf("/v1/records/%s/resolve", id), input)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	return printEnvelope(resp)
}
