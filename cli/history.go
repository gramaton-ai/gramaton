package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var (
	historyLimit   int
	historySince   string
	historyUntil   string
	historyActions []string
)

var historyCmd = &cobra.Command{
	Use:   "history <record-id>",
	Short: "Show per-record change history",
	Long: `Walks the commit chain backward, returning commits where the
record's stored hash changed. Equivalent to gramaton_history. Pass
--since / --until to narrow the walk via the D7 timestamp index;
date-bounded calls bypass the traversal cap.

Examples:
  gramaton history 01K...
  gramaton history 01K... --since 2026-04-20
  gramaton history 01K... --since 2026-04-20 --until 2026-04-22
  gramaton history 01K... --action resolve`,
	Args: cobra.ExactArgs(1),
	RunE: runHistory,
}

func init() {
	historyCmd.Flags().IntVar(&historyLimit, "limit", 20, "max entries to return")
	historyCmd.Flags().StringVar(&historySince, "since", "", "only changes on or after this date (YYYY-MM-DD or RFC3339)")
	historyCmd.Flags().StringVar(&historyUntil, "until", "", "only changes up to this date (YYYY-MM-DD or RFC3339); empty = HEAD")
	historyCmd.Flags().StringSliceVar(&historyActions, "action", nil, "filter by CommitAction.Kind (repeat for multiple)")
	rootCmd.AddCommand(historyCmd)
}

func runHistory(cmd *cobra.Command, args []string) error {
	id := args[0]
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", historyLimit))
	if historySince != "" {
		params.Set("since", historySince)
	}
	if historyUntil != "" {
		params.Set("until", historyUntil)
	}
	for _, a := range historyActions {
		params.Add("action", a)
	}

	path := fmt.Sprintf("/v1/records/%s/history?%s", url.PathEscape(id), params.Encode())
	resp, err := serverGet(path)
	if err != nil {
		return writeError("history_error", fmt.Sprintf("history: %s", err), false)
	}
	return printEnvelope(resp)
}
