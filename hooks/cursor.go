package hooks

import (
	"encoding/json"
	"fmt"
	"io"
)

// Cursor lifecycle-event handlers. Cursor follows Claude Code's hook
// protocol (stdin JSON, exit codes, fail-open handlers) but renames
// the identifiers: `conversation_id` instead of `session_id`, and
// `workspace_roots` (array) instead of `cwd`. These handlers decode
// Cursor's stdin shape into the shared HookInput and route to the
// same claude-protocol cores Claude Code uses, so the two harnesses
// can't drift apart behaviorally.
//
// Event mapping (camelCase names per Cursor's hooks.json schema,
// verified from the vendor-shipped create-hook skill, 2026-06-09):
//
//	sessionStart → CursorSessionStart → sessionStartCore
//	stop         → CursorStop         → stopCore
//	preCompact   → CursorPreCompact   → preCompactCore
//
// Cursor has no postCompact event, so there is no Cursor counterpart
// to ClaudeCodePostCompact.

// cursorInput is the subset of Cursor's hook stdin contract we
// consume. Every Cursor hook receives at least conversation_id,
// workspace_roots, hook_event_name, and transcript_path.
type cursorInput struct {
	ConversationID string   `json:"conversation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	TranscriptPath string   `json:"transcript_path"`
}

// decodeCursorInput reads Cursor's stdin shape and normalizes it to
// HookInput: conversation_id becomes the client session id,
// workspace_roots[0] becomes the cwd (Cursor can have multiple
// workspace roots; the first is the primary, and our per-cwd session
// tracking wants exactly one). Empty stdin is not an error
// (returns zero value); malformed JSON is.
func decodeCursorInput(r io.Reader) (HookInput, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return HookInput{}, fmt.Errorf("read stdin: %w", err)
	}
	var in cursorInput
	if len(body) == 0 {
		return HookInput{}, nil
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return HookInput{}, fmt.Errorf("parse stdin JSON: %w", err)
	}
	out := HookInput{
		SessionID:      in.ConversationID,
		TranscriptPath: in.TranscriptPath,
	}
	if len(in.WorkspaceRoots) > 0 {
		out.Cwd = in.WorkspaceRoots[0]
	}
	return out, nil
}

// CursorSessionStart handles Cursor's sessionStart event: starts or
// resumes the Gramaton session keyed by conversation_id and primes
// the pointer files + turn counter. See sessionStartCore.
func CursorSessionStart(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("cursor-session-start")
	defer log.Close()

	in, err := decodeCursorInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	sessionStartCore(log, in)
}

// CursorStop handles Cursor's stop event (agent completion):
// increments the turn counter. See stopCore.
func CursorStop(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("cursor-stop")
	defer log.Close()

	in, err := decodeCursorInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	stopCore(log, in)
}

// CursorPreCompact handles Cursor's preCompact event: archives the
// transcript and leaves the uncaptured-segments nudge flag when the
// current session has segments that were never saved. See
// preCompactCore.
func CursorPreCompact(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("cursor-pre-compact")
	defer log.Close()

	in, err := decodeCursorInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	preCompactCore(log, in)
}
