#!/bin/bash
# Claude Code PreCompact hook: checks for uncaptured segments before compaction.
# This is the critical safety net -- compaction destroys context, so we extract
# any pending knowledge first.
# Receives JSON on stdin with session_id (Claude Code's client session id).
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
GRAMATON="${GRAMATON_BIN:-gramaton}"
STATE_FILE="$HOME/.gramaton/hook-state/current-session.json"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) pre-compact: $*" >> "$LOG"; }

INPUT=$(cat)
CLIENT_SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

if [ -z "$CLIENT_SESSION_ID" ]; then
    log "no session_id in input, skipping"
    exit 0
fi

if ! command -v "$GRAMATON" &>/dev/null; then
    log "ERROR: gramaton CLI not found"
    exit 0
fi

# Resolve the gramaton session id. The SessionStart hook writes it to a
# well-known file alongside the client session id; "gramaton session get"
# expects the gramaton id, not the client id.
if [ ! -f "$STATE_FILE" ]; then
    log "ERROR: $STATE_FILE not found (SessionStart hook may not have run)"
    exit 0
fi
GRAMATON_SESSION_ID=$(python3 -c "import sys,json; print(json.load(open('$STATE_FILE')).get('session_id',''))" 2>/dev/null || echo "")
if [ -z "$GRAMATON_SESSION_ID" ]; then
    log "ERROR: no session_id in $STATE_FILE"
    exit 0
fi

# Get session state to check for uncaptured segments.
log "checking gramaton session $GRAMATON_SESSION_ID (client $CLIENT_SESSION_ID) for uncaptured segments"
STATE=$("$GRAMATON" session get "$GRAMATON_SESSION_ID" 2>>"$LOG") || {
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
