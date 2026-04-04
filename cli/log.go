package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var (
	logLast   int
	logRecord string
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show commit history",
	Long: `Displays the chain of commits from HEAD.

With --record, shows only commits that affected a specific node,
including what changed at each step.`,
	RunE: runLog,
}

func init() {
	logCmd.Flags().IntVar(&logLast, "last", 20, "number of commits to walk")
	logCmd.Flags().StringVar(&logRecord, "record", "", "show history for a specific record ID")
	rootCmd.AddCommand(logCmd)
}

func runLog(cmd *cobra.Command, args []string) error {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", logLast))

	if logRecord != "" {
		params.Set("record", logRecord)
	}

	path := "/v1/log?" + params.Encode()

	resp, err := serverGet(path)
	if err != nil {
		return writeError("log_error", fmt.Sprintf("log: %s", err), false)
	}

	return printEnvelope(resp)
}
