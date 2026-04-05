package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var (
	exportFormat string
	exportOutput string
)

var exportCmd = &cobra.Command{
	Use:   "export [query]",
	Short: "Export records from the knowledge store",
	Long: `Exports records in JSON Lines, CSV, or Markdown format.
Accepts the same search filters as the search command.
Without filters, exports all records.

Examples:
  gramaton export --format json > backup.jsonl
  gramaton export --format csv --output records.csv
  gramaton export --format markdown --keywords auth`,
	Args: cobra.MaximumNArgs(1),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "output format: json, csv, markdown")
	exportCmd.Flags().StringVar(&exportOutput, "output", "", "output file (default: stdout)")
	addSearchFlags(exportCmd)
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	body := buildSearchBody(cmd, args)
	body["format"] = exportFormat

	url, err := serverURL()
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("export: marshal: %w", err)
	}

	resp, err := http.Post(url+"/v1/export", "application/json",
		bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("export: server returned %d: %s", resp.StatusCode, respBody)
	}

	var w io.Writer = os.Stdout
	if exportOutput != "" {
		f, err := os.Create(exportOutput)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	_, err = io.Copy(w, resp.Body)
	return err
}
