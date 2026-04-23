#!/bin/bash
# Kiro UserPromptSubmit hook: checks turn counter, injects extraction reminder
# if threshold reached. Kiro's UserPromptSubmit can inject text into context.
# Receives JSON on stdin.
# Output (stdout) is injected into the agent's context.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
COUNTER_DIR="$HOME/.gramaton/hook-state"
THRESHOLD="${GRAMATON_EXTRACT_INTERVAL:-10}"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) user-prompt-submit: $*" >> "$LOG"; }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session_id') or d.get('agent_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    exit 0
fi

# SESSION_ID is used below as a filesystem path component.
case "$SESSION_ID" in
    *[!A-Za-z0-9_-]*) exit 0 ;;
esac

COUNTER_FILE="$COUNTER_DIR/$SESSION_ID.count"

COUNT=0
if [ -f "$COUNTER_FILE" ]; then
    COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo "0")
    if ! [[ "$COUNT" =~ ^[0-9]+$ ]]; then
        COUNT=0
    fi
fi

# Check if we've hit the threshold.
if [ "$COUNT" -ge "$THRESHOLD" ]; then
    # Output the reminder (injected into context by Kiro).
    cat <<'REMINDER'
[Gramaton reminder: You have been working for a while without extracting knowledge. Consider calling gramaton_session_prepare to review what should be captured, then gramaton_session_commit to save it.]
REMINDER
    log "reminder injected at turn $COUNT (threshold $THRESHOLD) for session $SESSION_ID"
    # Reset counter.
    echo "0" > "$COUNTER_FILE"
fi
