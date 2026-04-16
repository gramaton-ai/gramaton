package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var sessionClientID string
var sessionCommitFile string

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage knowledge capture sessions",
	Long: `Session commands for the two-phase knowledge extraction flow.

Use 'start' to create or resume a session, 'prepare' to get extraction
instructions, and 'commit' to submit extracted segments.`,
}

var sessionStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start or resume a session",
	Long: `Creates a new session or returns the existing one for the same
client ID (idempotent for --continue). No lookback to previous sessions.`,
	RunE: runSessionStart,
}

var sessionGetCmd = &cobra.Command{
	Use:   "get <session-id>",
	Short: "Get session state",
	Long:  `Returns the full session state including all topics and segments.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionGet,
}

var sessionPrepareCmd = &cobra.Command{
	Use:   "prepare <session-id>",
	Short: "Prepare for knowledge extraction",
	Long: `Returns extraction instructions and current session state.
Sets the server-side prepared flag required before commit.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionPrepare,
}

var sessionCommitCmd = &cobra.Command{
	Use:   "commit <session-id>",
	Short: "Submit extracted segments",
	Long: `Submits extracted knowledge segments to the session. Requires
prepare to have been called first. Reads segments from --file (JSON).`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionCommit,
}

func init() {
	sessionStartCmd.Flags().StringVar(&sessionClientID, "client-id", "", "client session identifier (required)")
	sessionStartCmd.MarkFlagRequired("client-id")

	sessionCommitCmd.Flags().StringVarP(&sessionCommitFile, "file", "f", "", "JSON file containing segments array (required)")
	sessionCommitCmd.MarkFlagRequired("file")

	sessionCmd.AddCommand(sessionStartCmd, sessionGetCmd, sessionPrepareCmd, sessionCommitCmd)
	rootCmd.AddCommand(sessionCmd)
}

func runSessionStart(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"client_session_id": sessionClientID,
	}
	resp, err := serverPost("/v1/sessions", body)
	if err != nil {
		return fmt.Errorf("session start: %w", err)
	}
	return printEnvelope(resp)
}

func runSessionGet(cmd *cobra.Command, args []string) error {
	resp, err := serverGet(fmt.Sprintf("/v1/sessions/%s", args[0]))
	if err != nil {
		return fmt.Errorf("session get: %w", err)
	}
	return printEnvelope(resp)
}

func runSessionPrepare(cmd *cobra.Command, args []string) error {
	resp, err := serverPost(fmt.Sprintf("/v1/sessions/%s/prepare", args[0]), nil)
	if err != nil {
		return fmt.Errorf("session prepare: %w", err)
	}
	return printEnvelope(resp)
}

func runSessionCommit(cmd *cobra.Command, args []string) error {
	var input map[string]any
	if err := readInputJSON(sessionCommitFile, &input, defaultLimits()); err != nil {
		return fmt.Errorf("input_error: %s", err)
	}
	// Wrap segments in commit payload with session_id.
	body := map[string]any{
		"session_id": args[0],
	}
	if segs, ok := input["segments"]; ok {
		body["segments"] = segs
	} else {
		return fmt.Errorf("input_error: JSON must contain a \"segments\" array")
	}

	resp, err := serverPost(fmt.Sprintf("/v1/sessions/%s/commit", args[0]), body)
	if err != nil {
		return fmt.Errorf("session commit: %w", err)
	}
	return printEnvelope(resp)
}
