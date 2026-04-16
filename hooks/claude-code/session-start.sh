#!/bin/bash
# Claude Code SessionStart hook: creates or resumes a Gramaton session.
# Receives JSON on stdin with session_id, cwd, etc.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
GRAMATON="${GRAMATON_BIN:-gramaton}"
COUNTER_DIR="$HOME/.gramaton/hook-state"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) session-start: $*" >> "$LOG"; }

# Read stdin.
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    log "no session_id in input, skipping"
    exit 0
fi

# Check gramaton CLI is available.
if ! command -v "$GRAMATON" &>/dev/null; then
    log "ERROR: gramaton CLI not found at $GRAMATON"
    exit 0  # Don't block the client.
fi

# Create or resume session.
log "starting session client_id=$SESSION_ID"
RESULT=$("$GRAMATON" session start --client-id "$SESSION_ID" 2>>"$LOG") || true
log "session created/resumed: $(echo "$RESULT" | head -1)"

# Reset turn counter.
mkdir -p "$COUNTER_DIR"
echo "0" > "$COUNTER_DIR/$SESSION_ID.count"
log "turn counter reset for session $SESSION_ID"
