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
SOURCE=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('source','startup'))" 2>/dev/null || echo "startup")
CWD=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null || echo "")

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
log "starting session client_id=$SESSION_ID source=$SOURCE"
RESULT=$("$GRAMATON" session start --client-id "$SESSION_ID" --source "$SOURCE" 2>>"$LOG") || true
log "session created/resumed: $(echo "$RESULT" | head -1)"

# Extract gramaton session ID from result and write to well-known file.
GRAMATON_SESSION_ID=$(echo "$RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
mkdir -p "$COUNTER_DIR"
if [ -n "$GRAMATON_SESSION_ID" ]; then
    # Legacy shared file -- still written so older agents/CLAUDE.md
    # instructions keep working. Last-writer-wins under concurrent
    # Claude Code instances; the per-cwd file below is the disambig.
    cat > "$COUNTER_DIR/current-session.json" <<ENDJSON
{"session_id": "$GRAMATON_SESSION_ID", "client_session_id": "$SESSION_ID"}
ENDJSON
    log "wrote current-session.json: gramaton=$GRAMATON_SESSION_ID client=$SESSION_ID"

    # Per-cwd canonical file. The slug strips the leading slash and
    # replaces remaining slashes with dashes, giving each working
    # directory its own file. `gramaton session current` uses this
    # to find the right session for $PWD even with concurrent
    # Claude Code instances.
    if [ -n "$CWD" ]; then
        CWD_SLUG=$(echo "$CWD" | sed 's|^/||; s|/|-|g')
        BY_CWD_DIR="$COUNTER_DIR/by-cwd"
        mkdir -p "$BY_CWD_DIR"
        cat > "$BY_CWD_DIR/$CWD_SLUG.session.json" <<ENDJSON
{"session_id": "$GRAMATON_SESSION_ID", "client_session_id": "$SESSION_ID", "cwd": "$CWD"}
ENDJSON
        log "wrote by-cwd file: $BY_CWD_DIR/$CWD_SLUG.session.json"
    fi
fi

# Reset turn counter.
echo "0" > "$COUNTER_DIR/$SESSION_ID.count"
log "turn counter reset for session $SESSION_ID"
