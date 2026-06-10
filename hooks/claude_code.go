package hooks

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// ClaudeCodeSessionStart handles Claude Code's SessionStart lifecycle
// event. Resumes or creates a Gramaton session and primes the counter
// + per-cwd pointer files that the other hooks read from.
//
// Stdin: JSON with session_id (Claude Code's client id), optional
// source (default "startup"), optional cwd.
//
// Side effects when session_id validates and `gramaton session start`
// returns a gramaton id:
//   - ~/.gramaton/hook-state/current-session.json (legacy shared file)
//   - ~/.gramaton/hook-state/by-cwd/<slug>.session.json (per-cwd)
//   - ~/.gramaton/hook-state/<client_id>.count → "0"
//
// Errors are logged and swallowed so the calling agent never fails
// because of a Gramaton bug.
func ClaudeCodeSessionStart(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("session-start")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	sessionStartCore(log, in)
}

// sessionStartCore is the claude-protocol SessionStart behavior,
// shared by every harness whose stdin normalizes to HookInput
// (Claude Code natively; Cursor via decodeCursorInput). Kiro's
// AgentSpawn stays separate -- it has agent_id fallback and no cwd.
func sessionStartCore(log *Logger, in HookInput) {
	sessionID := in.SessionID
	if sessionID == "" {
		log.Info("no session_id in input, skipping")
		return
	}
	if !ValidSessionID(sessionID) {
		log.Info("session_id has unsafe shape, skipping")
		return
	}
	source := in.Source
	if source == "" {
		source = "startup"
	}

	log.Info("starting session client_id=%s source=%s", sessionID, source)
	out, err := RunGramaton("session", "start", "--client-id", sessionID, "--source", source)
	if err != nil {
		// Legacy shell used `|| true` — continue to reset the
		// counter even if the call failed. Gramaton server may
		// be unavailable; we still want state primed for when
		// it comes back.
		log.Info("session start call failed: %v: %s", err, strings.TrimSpace(out))
	} else {
		log.Info("session created/resumed: %s", firstLine(out))
	}

	// Best-effort parse of the response to extract the gramaton session ID.
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &result)
	gramatonID := result.ID

	if gramatonID != "" {
		writeCurrentSession(log, gramatonID, sessionID)
		if in.Cwd != "" {
			writePerCwdSession(log, gramatonID, sessionID, in.Cwd)
		}
	}

	if err := ResetCounter(sessionID); err != nil {
		log.Info("reset counter: %v", err)
	} else {
		log.Info("turn counter reset for session %s", sessionID)
	}
}

// ClaudeCodeStop handles Claude Code's Stop lifecycle event:
// increments the turn counter for periodic extraction triggers.
//
// Stdin: JSON with session_id.
// Side effects: ~/.gramaton/hook-state/<client_id>.count += 1.
func ClaudeCodeStop(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("stop")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	stopCore(log, in)
}

// stopCore is the claude-protocol Stop behavior (turn-counter
// increment), shared by Claude Code and Cursor.
func stopCore(log *Logger, in HookInput) {
	sessionID := in.SessionID
	if sessionID == "" {
		// Legacy behavior: silent exit for non-session contexts.
		return
	}
	if !ValidSessionID(sessionID) {
		log.Info("session_id has unsafe shape, skipping")
		return
	}

	n, err := IncrementCounter(sessionID)
	if err != nil {
		log.Info("increment counter: %v", err)
		return
	}
	log.Info("turn counter for %s now %d", sessionID, n)
}

// preCompactFlag is what we write to the precompact-uncaptured flag
// file. Shape matches what gramaton session prepare reads.
type preCompactFlag struct {
	Count       int    `json:"count"`
	WarnedAt    string `json:"warned_at"`
	ArchivePath string `json:"archive_path"`
}

// ClaudeCodePreCompact handles Claude Code's PreCompact lifecycle
// event. When uncaptured segments exist in the current gramaton
// session, archives the raw transcript (so agent-driven extraction
// after compaction still has something to work with) and leaves a
// flag file that gramaton_session_prepare surfaces as a
// pending_uncaptured nudge.
//
// Stdin: JSON with session_id (client) and transcript_path.
func ClaudeCodePreCompact(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("pre-compact")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	preCompactCore(log, in)
}

// preCompactCore is the claude-protocol PreCompact behavior
// (archive transcript + leave the uncaptured-segments nudge flag),
// shared by Claude Code and Cursor.
func preCompactCore(log *Logger, in HookInput) {
	clientID := in.SessionID
	if clientID == "" {
		log.Info("no session_id in input, skipping")
		return
	}
	if !ValidSessionID(clientID) {
		log.Info("client session_id has unsafe shape, skipping")
		return
	}

	// Resolve the gramaton session id from the state SessionStart
	// wrote. Prefer the per-cwd binding: with multiple hooked
	// harnesses (or multiple instances in different directories)
	// the global pointer is last-writer-wins and can belong to a
	// different session — archiving this transcript there would
	// attach it to the wrong conversation. Fall back to the global
	// pointer for payloads without a cwd.
	gramatonID := ""
	if in.Cwd != "" {
		id, err := readPerCwdGramatonSessionID(in.Cwd)
		if err != nil {
			log.Info("resolve per-cwd session id: %v", err)
		} else if id != "" {
			gramatonID = id
		}
	}
	if gramatonID == "" {
		id, err := readCurrentGramatonSessionID()
		if err != nil {
			log.Info("resolve gramaton session id: %v", err)
			return
		}
		gramatonID = id
	}
	if gramatonID == "" {
		log.Info("no session bound (SessionStart may not have run)")
		return
	}

	log.Info("checking gramaton session %s (client %s) for uncaptured segments", gramatonID, clientID)
	stateOut, err := RunGramaton("session", "get", gramatonID)
	if err != nil {
		log.Info("session get failed: %v: %s", err, strings.TrimSpace(stateOut))
		return
	}

	uncaptured := countUncaptured(stateOut)
	log.Info("uncaptured segments: %d", uncaptured)
	if uncaptured == 0 {
		return
	}

	archivePath := ""
	if in.TranscriptPath != "" {
		log.Info("archiving transcript %s for session %s", in.TranscriptPath, gramatonID)
		archiveOut, archErr := RunGramaton("session", "archive", gramatonID, "--file", in.TranscriptPath)
		if archErr != nil {
			log.Info("archive call failed: %v: %s (continuing to write nudge flag)", archErr, strings.TrimSpace(archiveOut))
		} else {
			var a struct {
				ArchivePath string `json:"archive_path"`
			}
			_ = json.Unmarshal([]byte(archiveOut), &a)
			archivePath = a.ArchivePath
			log.Info("archive created: %s", archivePath)
		}
	} else {
		log.Info("no transcript_path in input; skipping archive")
	}

	flagPath, err := PreCompactFlagPath(clientID)
	if err != nil {
		log.Info("resolve precompact-uncaptured path: %v", err)
		return
	}
	flag := preCompactFlag{
		Count:       uncaptured,
		WarnedAt:    time.Now().UTC().Format(time.RFC3339),
		ArchivePath: archivePath,
	}
	if err := WriteJSON(flagPath, flag, 0o600); err != nil {
		log.Info("write precompact-uncaptured flag: %v", err)
		return
	}
	log.Info("wrote precompact-uncaptured flag: %s (count=%d)", flagPath, uncaptured)
}

// ClaudeCodePostCompact handles Claude Code's PostCompact lifecycle
// event: leaves a flag file that the next `gramaton session prepare`
// call surfaces so the agent knows the context was just compacted.
//
// Stdin: JSON with session_id.
// Side effects: ~/.gramaton/hook-state/<session_id>.compacted with
// RFC3339 timestamp.
func ClaudeCodePostCompact(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("post-compact")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	sessionID := in.SessionID
	if sessionID == "" {
		log.Info("no session_id in input, skipping")
		return
	}
	if !ValidSessionID(sessionID) {
		log.Info("session_id has unsafe shape, skipping")
		return
	}

	flagPath, err := PostCompactFlagPath(sessionID)
	if err != nil {
		log.Info("resolve post-compact flag path: %v", err)
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if err := atomicWriteFile(flagPath, []byte(ts+"\n"), 0o600); err != nil {
		log.Info("write post-compact flag: %v", err)
		return
	}
	log.Info("compaction flag written: %s", flagPath)
}

// writeCurrentSession writes the legacy shared pointer file
// ~/.gramaton/hook-state/current-session.json.
func writeCurrentSession(log *Logger, gramatonID, clientID string) {
	p, err := CurrentSessionPath()
	if err != nil {
		log.Info("resolve current-session path: %v", err)
		return
	}
	payload := map[string]string{
		"session_id":        gramatonID,
		"client_session_id": clientID,
	}
	if err := WriteJSON(p, payload, 0o600); err != nil {
		log.Info("write current-session.json: %v", err)
		return
	}
	log.Info("wrote current-session.json: gramaton=%s client=%s", gramatonID, clientID)
}

// writePerCwdSession writes the per-cwd session pointer file so
// concurrent Claude Code instances don't overwrite each other's
// session records. `gramaton session current` looks up this file
// to find the right session for $PWD.
func writePerCwdSession(log *Logger, gramatonID, clientID, cwd string) {
	p, err := PerCwdSessionPath(cwd)
	if err != nil {
		log.Info("resolve per-cwd path: %v", err)
		return
	}
	payload := map[string]string{
		"session_id":        gramatonID,
		"client_session_id": clientID,
		"cwd":               cwd,
	}
	if err := WriteJSON(p, payload, 0o600); err != nil {
		log.Info("write per-cwd session file: %v", err)
		return
	}
	log.Info("wrote by-cwd file: %s", p)
}

// readPerCwdGramatonSessionID returns the session_id from the
// per-cwd pointer file SessionStart wrote for cwd. Empty string if
// the file is missing (caller falls back to the global pointer);
// error only on a malformed file.
func readPerCwdGramatonSessionID(cwd string) (string, error) {
	p, err := PerCwdSessionPath(cwd)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", nil // missing is fine; caller decides
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	return payload.SessionID, nil
}

// readCurrentGramatonSessionID returns the session_id field from
// ~/.gramaton/hook-state/current-session.json. Empty string if the
// file is missing or malformed.
func readCurrentGramatonSessionID() (string, error) {
	p, err := CurrentSessionPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", nil // missing is fine; caller decides
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	return payload.SessionID, nil
}

// countUncaptured counts segments in a `gramaton session get`
// response that lack a captured_as field. Matches the legacy shell
// script's Python one-liner.
func countUncaptured(stateJSON string) int {
	var state struct {
		Topics []struct {
			Segments []struct {
				CapturedAs string `json:"captured_as"`
			} `json:"segments"`
		} `json:"topics"`
	}
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return 0
	}
	n := 0
	for _, t := range state.Topics {
		for _, s := range t.Segments {
			if s.CapturedAs == "" {
				n++
			}
		}
	}
	return n
}

// firstLine returns the first line of s, trimmed. Mirrors
// `head -1` in the legacy shell scripts' log lines.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
