package hooks

import (
	"fmt"
	"io"
	"strings"
)

// KiroAgentSpawn handles Kiro's AgentSpawn lifecycle event. Behaves
// like Claude Code's SessionStart but: (a) session_id falls back to
// agent_id if missing (Kiro payloads use agent_id), and (b) Kiro
// doesn't currently send a cwd field, so per-cwd session tracking
// is skipped.
//
// Stdin: JSON with session_id OR agent_id.
// Side effects: calls `gramaton session start --client-id <id>`
// (Kiro has no source field — we omit --source so the server picks
// its default) and resets the turn counter.
func KiroAgentSpawn(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("agent-spawn")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	id := in.ResolvedSessionID()
	if id == "" {
		log.Info("no session_id or agent_id in input, skipping")
		return
	}
	if !ValidSessionID(id) {
		log.Info("session_id has unsafe shape, skipping")
		return
	}

	log.Info("starting session client_id=%s", id)
	out, err := RunGramaton("session", "start", "--client-id", id)
	if err != nil {
		log.Info("session start failed: %v: %s", err, strings.TrimSpace(out))
	} else {
		log.Info("session created/resumed: %s", firstLine(out))
	}

	if err := ResetCounter(id); err != nil {
		log.Info("reset counter: %v", err)
	} else {
		log.Info("turn counter reset for session %s", id)
	}
}

// extractionReminder is the text Kiro's UserPromptSubmit hook
// injects into the agent's context when the turn counter crosses
// the threshold. Preserved verbatim from the legacy shell script
// so agents see the same nudge.
const extractionReminder = `[Gramaton reminder: You have been working for a while without extracting knowledge. Consider calling gramaton_session_prepare to review what should be captured, then gramaton_session_commit to save it.]`

// KiroUserPromptSubmit handles Kiro's UserPromptSubmit event.
// Kiro's unique contract for this hook: whatever the hook writes
// to stdout is injected into the agent's next-prompt context.
//
// We check the turn counter. If it's >= ExtractThreshold (env
// override GRAMATON_EXTRACT_INTERVAL or default 10), write the
// extraction reminder to stdout AND reset the counter so the
// nudge doesn't fire on every subsequent turn.
//
// Stdin: JSON with session_id OR agent_id.
// Stdout: extraction reminder text when threshold crossed.
func KiroUserPromptSubmit(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("user-prompt-submit")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	id := in.ResolvedSessionID()
	if id == "" {
		// Silent exit matches legacy behavior for non-session contexts.
		return
	}
	if !ValidSessionID(id) {
		// Silent exit matches legacy behavior for unsafe ids.
		return
	}

	count := ReadCounter(id)
	threshold := ExtractThreshold()
	if count < threshold {
		return
	}

	if _, err := fmt.Fprintln(stdout, extractionReminder); err != nil {
		log.Info("write reminder to stdout: %v", err)
		return
	}
	log.Info("reminder injected at turn %d (threshold %d) for session %s", count, threshold, id)

	if err := ResetCounter(id); err != nil {
		log.Info("reset counter: %v", err)
	}
}

// KiroStop handles Kiro's Stop lifecycle event: increments the turn
// counter that KiroUserPromptSubmit checks. Equivalent to Claude
// Code's Stop handler but accepts the Kiro agent_id fallback.
//
// Stdin: JSON with session_id OR agent_id.
// Side effects: ~/.gramaton/hook-state/<id>.count += 1.
func KiroStop(stdin io.Reader, stdout io.Writer) {
	log := OpenLogger("kiro-stop")
	defer log.Close()

	in, err := DecodeInput(stdin)
	if err != nil {
		log.Info("parse stdin: %v", err)
		return
	}
	id := in.ResolvedSessionID()
	if id == "" {
		return
	}
	if !ValidSessionID(id) {
		return
	}

	n, err := IncrementCounter(id)
	if err != nil {
		log.Info("increment counter: %v", err)
		return
	}
	log.Info("turn counter for %s now %d", id, n)
}
