#!/bin/bash
# Claude Code PostCompact hook: writes a flag file so the next prepare call
# knows to include a reminder about recently-compacted context.
# Receives JSON on stdin with session_id.
# Logs to ~/.gramaton/hooks.log.

set -euo pipefail

mkdir -p "$HOME/.gramaton"
LOG="$HOME/.gramaton/hooks.log"
FLAG_DIR="$HOME/.gramaton/hook-state"

log() { echo "[gramaton-hook] $(date -u +%Y-%m-%dT%H:%M:%SZ) post-compact: $*" >> "$LOG"; }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

if [ -z "$SESSION_ID" ]; then
    log "no session_id in input, skipping"
    exit 0
fi

# SESSION_ID is used below as a filesystem path component.
case "$SESSION_ID" in
    *[!A-Za-z0-9_-]*)
        log "SESSION_ID has unsafe shape, skipping"
        exit 0
        ;;
esac

mkdir -p "$FLAG_DIR"
FLAG_FILE="$FLAG_DIR/$SESSION_ID.compacted"
date -u +%Y-%m-%dT%H:%M:%SZ > "$FLAG_FILE"
log "compaction flag written: $FLAG_FILE"
