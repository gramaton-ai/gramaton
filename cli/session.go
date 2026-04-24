package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gramaton-ai/gramaton/hooks"
)

var sessionClientID string
var sessionSource string
var sessionCommitFile string
var sessionArchiveFile string

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

var sessionArchiveCmd = &cobra.Command{
	Use:   "archive <session-id>",
	Short: "Archive raw conversation text",
	Long: `Compresses a conversation transcript and stores it as a gzip
archive referenced from the session. The archive is NOT indexed or
searchable -- it is a break-glass backup of the raw conversation.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionArchive,
}

var sessionCurrentCwd string

var sessionCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Print the active session for this working directory",
	Long: `Resolves which Gramaton session is bound to the current working
directory and prints the session id and client session id as JSON.

The Claude Code SessionStart hook writes per-cwd files to
~/.gramaton/hook-state/by-cwd/. This command reads $PWD (or --cwd),
finds the matching file, and prints its contents. Falls back to the
shared current-session.json when no per-cwd file exists. Pure file
lookup -- no server interaction required.`,
	RunE: runSessionCurrent,
}

func init() {
	sessionStartCmd.Flags().StringVar(&sessionClientID, "client-id", "", "client session identifier (required)")
	sessionStartCmd.MarkFlagRequired("client-id")
	sessionStartCmd.Flags().StringVar(&sessionSource, "source", "", "startup or resume (controls session chaining)")

	sessionCommitCmd.Flags().StringVarP(&sessionCommitFile, "file", "f", "", "JSON file containing segments array (required)")
	sessionCommitCmd.MarkFlagRequired("file")

	sessionArchiveCmd.Flags().StringVarP(&sessionArchiveFile, "file", "f", "", "source file to archive (required)")
	sessionArchiveCmd.MarkFlagRequired("file")

	sessionCurrentCmd.Flags().StringVar(&sessionCurrentCwd, "cwd", "", "working directory to look up (default: $PWD)")

	sessionCmd.AddCommand(sessionStartCmd, sessionGetCmd, sessionPrepareCmd, sessionCommitCmd, sessionArchiveCmd, sessionCurrentCmd)
	rootCmd.AddCommand(sessionCmd)
}

func runSessionStart(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"client_session_id": sessionClientID,
		"source":            sessionSource,
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

// cwdSlug delegates to hooks.CwdSlug so the session-lookup slug and
// the hook-write slug stay in lockstep. Single source of truth now
// that hook logic lives in Go — pre-Phase-2 both sides had their
// own implementation (shell for the hook, Go here) and drift was
// possible.
func cwdSlug(cwd string) string {
	return hooks.CwdSlug(cwd)
}

// resolveCurrentSession looks up the active session for a given cwd by
// reading the per-cwd file written by session-start.sh, falling back
// to the legacy shared current-session.json. Returns the JSON payload
// as a parsed map, the path it came from, or an error if neither file
// exists or both are corrupt.
func resolveCurrentSession(base, cwd string) (map[string]any, string, error) {
	byCwd := filepath.Join(base, "hook-state", "by-cwd", cwdSlug(cwd)+".session.json")
	legacy := filepath.Join(base, "hook-state", "current-session.json")

	for _, candidate := range []string{byCwd, legacy} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, candidate, fmt.Errorf("%s is corrupt: %w", candidate, err)
		}
		return parsed, candidate, nil
	}

	return nil, "", fmt.Errorf("no session file for cwd %q (checked %s, %s) -- has the SessionStart hook run?", cwd, byCwd, legacy)
}

func runSessionCurrent(cmd *cobra.Command, args []string) error {
	cwd := sessionCurrentCwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("session current: cannot get working directory: %w", err)
		}
	}

	parsed, _, err := resolveCurrentSession(baseConfigDir(), cwd)
	if err != nil {
		return fmt.Errorf("session current: %w", err)
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("session current: marshal: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func runSessionArchive(cmd *cobra.Command, args []string) error {
	body := map[string]any{
		"session_id":  args[0],
		"source_path": sessionArchiveFile,
	}
	resp, err := serverPost(fmt.Sprintf("/v1/sessions/%s/archive", args[0]), body)
	if err != nil {
		return fmt.Errorf("session archive: %w", err)
	}
	return printEnvelope(resp)
}
