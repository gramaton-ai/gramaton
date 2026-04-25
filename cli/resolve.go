package cli

import (
	"fmt"
	"net/url"

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
	input, err := readCommandInput(resolveFile)
	if err != nil {
		return err
	}
	id, err := extractRequiredID(input)
	if err != nil {
		return err
	}

	resp, err := serverPost(fmt.Sprintf("/v1/records/%s/resolve", url.PathEscape(id)), input)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	return printEnvelope(resp)
}
