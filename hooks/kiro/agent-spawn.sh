#!/bin/bash
# Kiro AgentSpawn hook: creates or resumes a Gramaton session.
# Same as Claude Code SessionStart but triggered on agent spawn.
# Receives JSON on stdin.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
GRAMATON="${GRAMATON_BIN:-gramaton}"
COUNTER_DIR="$HOME/.gramaton/hook-state"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) agent-spawn: $*" >> "$LOG"; }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session_id') or d.get('agent_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    log "no session_id or agent_id in input, skipping"
    exit 0
fi

# SESSION_ID is used below as a filesystem path component.
case "$SESSION_ID" in
    *[!A-Za-z0-9_-]*)
        log "SESSION_ID has unsafe shape, skipping"
        exit 0
        ;;
esac

if ! command -v "$GRAMATON" &>/dev/null; then
    log "ERROR: gramaton CLI not found at $GRAMATON"
    exit 0
fi

log "starting session client_id=$SESSION_ID"
RESULT=$("$GRAMATON" session start --client-id "$SESSION_ID" 2>>"$LOG") || true
log "session created/resumed: $(echo "$RESULT" | head -1)"

mkdir -p "$COUNTER_DIR"
echo "0" > "$COUNTER_DIR/$SESSION_ID.count"
log "turn counter reset for session $SESSION_ID"
