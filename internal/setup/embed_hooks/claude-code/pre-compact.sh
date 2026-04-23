#!/bin/bash
# Claude Code PreCompact hook: preservation safety net before compaction.
#
# The hook has no LLM, so it cannot extract segments the way in-session
# prepare/commit does. What it CAN do, when uncaptured segments exist,
# is preserve the raw transcript via `gramaton session archive` and
# leave a flag file that the next gramaton_session_prepare surfaces as
# a pending_uncaptured nudge.
#
# Receives JSON on stdin with session_id (Claude Code's client session
# id) and transcript_path.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
GRAMATON="${GRAMATON_BIN:-gramaton}"
STATE_DIR="$HOME/.gramaton/hook-state"
STATE_FILE="$STATE_DIR/current-session.json"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) pre-compact: $*" >> "$LOG"; }

INPUT=$(cat)
CLIENT_SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")
TRANSCRIPT_PATH=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('transcript_path',''))" 2>/dev/null || echo "")

if [ -z "$CLIENT_SESSION_ID" ]; then
    log "no session_id in input, skipping"
    exit 0
fi

# CLIENT_SESSION_ID is used below as a filesystem path component.
case "$CLIENT_SESSION_ID" in
    *[!A-Za-z0-9_-]*)
        log "CLIENT_SESSION_ID has unsafe shape, skipping"
        exit 0
        ;;
esac

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

# Get session state to count uncaptured segments.
log "checking gramaton session $GRAMATON_SESSION_ID (client $CLIENT_SESSION_ID) for uncaptured segments"
STATE=$("$GRAMATON" session get "$GRAMATON_SESSION_ID" 2>>"$LOG") || {
    log "ERROR: failed to get session state"
    exit 0
}

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

if [ "$UNCAPTURED" -eq 0 ]; then
    # Nothing at risk; nothing to preserve.
    exit 0
fi

# We have uncaptured segments. Preserve the raw transcript and leave a
# nudge flag so the next prepare can surface it to the agent.
ARCHIVE_PATH=""
if [ -n "$TRANSCRIPT_PATH" ] && [ -f "$TRANSCRIPT_PATH" ]; then
    log "archiving transcript $TRANSCRIPT_PATH for session $GRAMATON_SESSION_ID"
    ARCHIVE_RESULT=$("$GRAMATON" session archive "$GRAMATON_SESSION_ID" --file "$TRANSCRIPT_PATH" 2>>"$LOG") || {
        log "WARNING: archive call failed (continuing to write nudge flag)"
        ARCHIVE_RESULT=""
    }
    if [ -n "$ARCHIVE_RESULT" ]; then
        ARCHIVE_PATH=$(echo "$ARCHIVE_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('archive_path',''))" 2>/dev/null || echo "")
        log "archive created: $ARCHIVE_PATH"
    fi
else
    log "WARNING: no transcript_path in input (or file missing); skipping archive"
fi

# Write the nudge flag. serviceSessionPrepare surfaces this (single-shot,
# 2h TTL) as pending_uncaptured on the next prepare response.
# Emit via python argv so ARCHIVE_PATH (gramaton CLI output) and TS
# can't corrupt the JSON.
mkdir -p "$STATE_DIR"
FLAG_FILE="$STATE_DIR/$CLIENT_SESSION_ID.precompact-uncaptured"
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
python3 -c 'import json,sys; sys.stdout.write(json.dumps({"count": int(sys.argv[1]), "warned_at": sys.argv[2], "archive_path": sys.argv[3]}))' \
    "$UNCAPTURED" "$TS" "$ARCHIVE_PATH" > "$FLAG_FILE"
log "wrote precompact-uncaptured flag: $FLAG_FILE (count=$UNCAPTURED)"
