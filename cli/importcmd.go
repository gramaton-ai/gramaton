package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/spf13/cobra"
)

var (
	importCSVFlag      bool
	importObsidianFlag bool
)

var importCmd = &cobra.Command{
	Use:   "import <file-or-directory>",
	Short: "Import records into the knowledge store",
	Long: `Imports records from JSON Lines files, CSV files, or Obsidian
vaults. Backup archives (.tar.gz) are detected and redirected to
the restore command.

Examples:
  gramaton import records.jsonl
  gramaton import --csv data.csv
  gramaton import --obsidian ~/notes/vault`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().BoolVar(&importCSVFlag, "csv", false, "import from CSV file")
	importCmd.Flags().BoolVar(&importObsidianFlag, "obsidian", false, "import from Obsidian vault directory")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	path := args[0]

	// Detect backup archives.
	if backup.IsBackupArchive(path) {
		return fmt.Errorf("detected backup archive. Use 'gramaton restore %s' instead", path)
	}

	if importObsidianFlag {
		return importObsidian(path)
	}

	// Read file and send to server.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	if importCSVFlag {
		return importCSV(data)
	}

	return importJSON(data)
}

func importJSON(data []byte) error {
	// Parse JSON Lines into records array.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var records []json.RawMessage
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		records = append(records, json.RawMessage(line))
	}

	if len(records) == 0 {
		return writeError("no_records", "no records found in file", false)
	}

	resp, err := serverPost("/v1/import", map[string]any{
		"records": records,
	})
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	return printEnvelope(resp)
}

func importCSV(data []byte) error {
	// TODO: implement CSV import (parse client-side, send as JSON
	// records to the import endpoint).
	return fmt.Errorf("CSV import via CLI is not yet implemented. Use the REST API directly: POST /v1/import")
}

func importObsidian(vaultPath string) error {
	// Obsidian import requires filesystem access. Since the CLI
	// is a thin client and the server may be on another machine,
	// we read the vault locally and send records via the API.

	// TODO: implement Obsidian import by reading .md files and
	// sending them as JSON records
	return fmt.Errorf("Obsidian import via CLI is not yet implemented. Use the REST API directly")
}
