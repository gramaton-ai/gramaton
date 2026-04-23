package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var (
	diffSince string
	diffUntil string
	diffTopic string
)

var diffCmd = &cobra.Command{
	Use:   "diff [commit1..commit2]",
	Short: "Show changes between commits",
	Long: `Compares two commits and shows what changed.

Usage:
  gramaton diff                                    # HEAD vs parent
  gramaton diff abc123..def456                     # two specific commits
  gramaton diff --since 2026-03-01                 # since a date
  gramaton diff --since 2026-03-01 --until 2026-03-07
  gramaton diff --topic "authentication"           # filter by topic`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDiff,
}

func init() {
	diffCmd.Flags().StringVar(&diffSince, "since", "", "show changes since this date (YYYY-MM-DD or RFC3339)")
	diffCmd.Flags().StringVar(&diffUntil, "until", "", "show changes up to this date (YYYY-MM-DD or RFC3339); empty = HEAD")
	diffCmd.Flags().StringVar(&diffTopic, "topic", "", "filter changes by topic (keyword + semantic match)")
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) error {
	params := url.Values{}

	if len(args) == 1 {
		params.Set("range", args[0])
	}
	if diffSince != "" {
		params.Set("since", diffSince)
	}
	if diffUntil != "" {
		params.Set("until", diffUntil)
	}
	if diffTopic != "" {
		params.Set("topic", diffTopic)
	}

	path := "/v1/diff"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := serverGet(path)
	if err != nil {
		return writeError("diff_error", fmt.Sprintf("diff: %s", err), false)
	}

	return printEnvelope(resp)
}
