#!/bin/bash
# Claude Code PreCompact hook: checks for uncaptured segments before compaction.
# This is the critical safety net -- compaction destroys context, so we extract
# any pending knowledge first.
# Receives JSON on stdin with session_id.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
GRAMATON="${GRAMATON_BIN:-gramaton}"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) pre-compact: $*" >> "$LOG"; }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    log "no session_id in input, skipping"
    exit 0
fi

if ! command -v "$GRAMATON" &>/dev/null; then
    log "ERROR: gramaton CLI not found"
    exit 0
fi

# Get session state to check for uncaptured segments.
log "checking session $SESSION_ID for uncaptured segments"
STATE=$("$GRAMATON" session get "$SESSION_ID" 2>>"$LOG") || {
    log "ERROR: failed to get session state"
    exit 0
}

# Count uncaptured segments (those without captured_as).
UNCAPTURED=$(echo "$STATE" | python3 -c "
import sys, json
data = json.load(sys.stdin)
count = 0
for topic in data.get('topics', []):
    for seg in topic.get('segments', []):
        if not seg.get('captured_as'):
            count += 1
print(count)
" 2>/dev/null || echo "0")

log "uncaptured segments: $UNCAPTURED"

if [ "$UNCAPTURED" -gt 0 ]; then
    log "WARNING: $UNCAPTURED uncaptured segments before compaction"
    # The agent should have extracted these already. Log the warning
    # so we can track how often this happens. The redundant nudge
    # mechanism (PostCompact) will handle recovery.
fi
