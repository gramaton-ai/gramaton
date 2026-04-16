#!/bin/bash
# Claude Code Stop hook: increments turn counter for periodic extraction triggers.
# Receives JSON on stdin with session_id.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
COUNTER_DIR="$HOME/.gramaton/hook-state"
THRESHOLD="${GRAMATON_EXTRACT_INTERVAL:-10}"  # Extract every N turns.

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) stop: $*" >> "$LOG"; }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    exit 0  # Silent exit for non-session contexts.
fi

mkdir -p "$COUNTER_DIR"
COUNTER_FILE="$COUNTER_DIR/$SESSION_ID.count"

# Read current count, default to 0.
COUNT=0
if [ -f "$COUNTER_FILE" ]; then
    COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo "0")
    # Handle corrupt counter file.
    if ! [[ "$COUNT" =~ ^[0-9]+$ ]]; then
        COUNT=0
    fi
fi

COUNT=$((COUNT + 1))
echo "$COUNT" > "$COUNTER_FILE"
