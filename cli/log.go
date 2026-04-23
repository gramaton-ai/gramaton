package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var (
	logLast            int
	logRecord          string
	logSince           string
	logUntil           string
	logActions         []string
	logExcludeCuration bool
	logIncludeRecords  bool
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show commit history",
	Long: `Displays the chain of commits.

Examples:
  gramaton log --last 20                         # last 20 commits
  gramaton log --since 2026-04-20                # commits since a date
  gramaton log --since 2026-04-20 --until 2026-04-21
  gramaton log --action resolve --action collection_update
  gramaton log --since 2026-04-22 --exclude-curation --include-records

For per-record change history, use 'gramaton history <id>' instead
(superseded --record on this command; --record still works for
source compatibility).`,
	RunE: runLog,
}

func init() {
	logCmd.Flags().IntVar(&logLast, "last", 20, "max commits to return")
	logCmd.Flags().StringVar(&logRecord, "record", "", "deprecated: use 'gramaton history <id>'")
	logCmd.Flags().StringVar(&logSince, "since", "", "only commits on or after this date (YYYY-MM-DD or RFC3339)")
	logCmd.Flags().StringVar(&logUntil, "until", "", "only commits up to this date (YYYY-MM-DD or RFC3339); empty = HEAD")
	logCmd.Flags().StringSliceVar(&logActions, "action", nil, "filter by CommitAction.Kind (repeat for multiple, e.g. --action resolve --action collection_update)")
	logCmd.Flags().BoolVar(&logExcludeCuration, "exclude-curation", false, "drop commits whose message starts with 'curation:'")
	logCmd.Flags().BoolVar(&logIncludeRecords, "include-records", false, "enrich each commit with per-record mutation summaries")
	rootCmd.AddCommand(logCmd)
}

func runLog(cmd *cobra.Command, args []string) error {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", logLast))

	if logRecord != "" {
		params.Set("record", logRecord)
	}
	if logSince != "" {
		params.Set("since", logSince)
	}
	if logUntil != "" {
		params.Set("until", logUntil)
	}
	for _, a := range logActions {
		params.Add("action", a)
	}
	if logExcludeCuration {
		params.Set("exclude_curation", "true")
	}
	if logIncludeRecords {
		params.Set("include_record_mutations", "true")
	}

	path := "/v1/log?" + params.Encode()

	resp, err := serverGet(path)
	if err != nil {
		return writeError("log_error", fmt.Sprintf("log: %s", err), false)
	}

	return printEnvelope(resp)
}
