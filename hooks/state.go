// Package hooks contains the handlers for Claude Code and Kiro
// agent-lifecycle events that Gramaton surfaces through the
// `gramaton hook <event>` subcommand.
//
// Before Phase 2 of the Windows platform-support plan, these hooks
// were bash scripts in ~/.gramaton/hooks/**/*.sh that parsed JSON
// via python3, maintained counter files, and shelled out to the
// gramaton CLI. The scripts carried a hidden python3 dependency
// and a read-modify-write race in the counter update. Phase 2
// moves the logic into Go: ~/.gramaton/hooks/**/*.sh becomes a
// one-line proxy that execs `gramaton hook <event>`; all real
// work lives here.
//
// The handlers are "fail open" — any error is logged to
// ~/.gramaton/hooks.log and the handler returns without error so
// the calling agent (Claude Code / Kiro) is never blocked by a
// Gramaton bug.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// HookInput is the union of JSON fields every hook may see on stdin.
// Unused fields remain empty (Go JSON silently ignores unknown
// fields, and our fields are all omitempty).
type HookInput struct {
	SessionID      string `json:"session_id"`
	AgentID        string `json:"agent_id"`
	Source         string `json:"source"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
}

// ResolvedSessionID returns SessionID if set, else AgentID. Kiro
// payloads prefer agent_id; Claude Code uses session_id.
func (h HookInput) ResolvedSessionID() string {
	if h.SessionID != "" {
		return h.SessionID
	}
	return h.AgentID
}

// DecodeInput reads and decodes a HookInput JSON object from r.
// Empty stdin is not an error (returns zero value); malformed JSON is.
func DecodeInput(r io.Reader) (HookInput, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return HookInput{}, fmt.Errorf("read stdin: %w", err)
	}
	var in HookInput
	if len(body) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return in, fmt.Errorf("parse stdin JSON: %w", err)
	}
	return in, nil
}

// validSessionID matches what the original shell scripts permitted
// via `case *[!A-Za-z0-9_-]*`. Session IDs flow into filesystem
// paths (counter files, per-cwd state), so anything outside this
// set is rejected to prevent path-traversal shapes.
var validSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidSessionID reports whether s is safe to use as a filesystem
// path component. Matches the legacy shell-script regex exactly.
func ValidSessionID(s string) bool {
	return s != "" && validSessionID.MatchString(s)
}

// GramatonDir is the base directory for Gramaton's per-user state.
// Override: $HOME is resolved via os.UserHomeDir().
func GramatonDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".gramaton"), nil
}

// HookStateDir returns ~/.gramaton/hook-state/, ensuring it exists.
// Hooks stash per-session counter files, the current-session
// pointer file, and precompact flag files here.
func HookStateDir() (string, error) {
	base, err := GramatonDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "hook-state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir hook-state: %w", err)
	}
	return dir, nil
}

// CounterPath returns the turn-counter file path for a session.
// Caller must have already validated sessionID via ValidSessionID.
func CounterPath(sessionID string) (string, error) {
	dir, err := HookStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".count"), nil
}

// ReadCounter returns the current turn count for a session.
// Returns 0 on missing or corrupt file — matches the legacy
// shell-script behavior of silently treating a corrupt counter
// as 0.
func ReadCounter(sessionID string) int {
	p, err := CounterPath(sessionID)
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// WriteCounter sets the turn counter for a session atomically
// (write to <counter>.tmp then rename).
func WriteCounter(sessionID string, n int) error {
	p, err := CounterPath(sessionID)
	if err != nil {
		return err
	}
	return atomicWriteFile(p, []byte(strconv.Itoa(n)+"\n"), 0o600)
}

// IncrementCounter performs a read-modify-write on the counter
// and returns the new value. The R-M-W is NOT atomic against
// concurrent hook invocations from other processes: if two Claude
// Code sessions fire their Stop hook simultaneously both may read
// the same value and each write N+1, losing an increment. The
// legacy shell scripts had the same race; Gramaton accepts it as
// the cost of cross-process coordination without pulling in a
// file-locking primitive (syscall.Flock / LockFileEx would need
// a platform split and the race loses at most one turn per
// simultaneous-fire, which nudges extraction slightly later —
// non-critical).
func IncrementCounter(sessionID string) (int, error) {
	n := ReadCounter(sessionID) + 1
	return n, WriteCounter(sessionID, n)
}

// ResetCounter writes 0 to the counter file atomically.
func ResetCounter(sessionID string) error {
	return WriteCounter(sessionID, 0)
}

// CurrentSessionPath is the legacy shared file ~/.gramaton/hook-state/
// current-session.json. Last-writer-wins under concurrent Claude Code
// instances; the per-cwd file (PerCwdSessionPath) disambiguates.
func CurrentSessionPath() (string, error) {
	dir, err := HookStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "current-session.json"), nil
}

// PerCwdSessionPath returns the per-cwd session pointer file. Each
// working directory gets its own file so concurrent Claude Code
// instances don't overwrite each other's session records.
func PerCwdSessionPath(cwd string) (string, error) {
	dir, err := HookStateDir()
	if err != nil {
		return "", err
	}
	byCwd := filepath.Join(dir, "by-cwd")
	if err := os.MkdirAll(byCwd, 0o700); err != nil {
		return "", fmt.Errorf("mkdir by-cwd: %w", err)
	}
	return filepath.Join(byCwd, CwdSlug(cwd)+".session.json"), nil
}

// PreCompactFlagPath returns the flag-file path Gramaton's session
// prepare surfaces as pending_uncaptured after a pre-compact hook.
// Caller must have already validated clientSessionID.
func PreCompactFlagPath(clientSessionID string) (string, error) {
	dir, err := HookStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, clientSessionID+".precompact-uncaptured"), nil
}

// PostCompactFlagPath returns the flag-file path for post-compact.
// Caller must have already validated sessionID.
func PostCompactFlagPath(sessionID string) (string, error) {
	dir, err := HookStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".compacted"), nil
}

// CwdSlug converts an absolute cwd path into a filename-safe token
// used for per-cwd session lookup. Works identically on Unix and
// Windows — must match cli/session.go's cwdSlug so hook-written
// files are findable by `gramaton session current`.
//
// Rules:
//   /foo/bar           → foo-bar
//   C:\Users\b\foo     → C-Users-b-foo
//   C:/Users/b/foo     → C-Users-b-foo     (if caller already forward-slashed)
//
// Do NOT use filepath.ToSlash here: it's a no-op on Unix so Windows
// inputs reaching a Unix build (tests, mixed environments) would
// pass through with backslashes intact. Manual replace is portable.
func CwdSlug(cwd string) string {
	s := strings.ReplaceAll(cwd, `\`, "/")
	s = strings.TrimPrefix(s, "/")
	// Handle the drive-letter compound "C:/" → "C-" before the
	// general colon/slash passes, otherwise ":" → "-" followed
	// by "/" → "-" produces "C--Users-..." (double dash).
	s = strings.ReplaceAll(s, ":/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return strings.ReplaceAll(s, "/", "-")
}

// ExtractThreshold returns the turn-counter threshold after which
// Kiro's user-prompt-submit hook injects an extraction reminder.
// Override via GRAMATON_EXTRACT_INTERVAL env var; default 10.
func ExtractThreshold() int {
	if v := os.Getenv("GRAMATON_EXTRACT_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 10
}

// Logger is an append-only writer for ~/.gramaton/hooks.log,
// prefixed per hook event.
type Logger struct {
	w       io.WriteCloser
	hookTag string
}

// OpenLogger appends to ~/.gramaton/hooks.log and tags every line
// with hookTag (e.g. "session-start"). Callers should defer Close.
// Returns a no-op logger if the log file can't be opened so hooks
// stay fail-open.
func OpenLogger(hookTag string) *Logger {
	base, err := GramatonDir()
	if err != nil {
		return &Logger{w: nopWriteCloser{}, hookTag: hookTag}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return &Logger{w: nopWriteCloser{}, hookTag: hookTag}
	}
	p := filepath.Join(base, "hooks.log")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return &Logger{w: nopWriteCloser{}, hookTag: hookTag}
	}
	return &Logger{w: f, hookTag: hookTag}
}

// Info writes "[gramaton-hook] <rfc3339> <hookTag>: <fmt>\n".
func (l *Logger) Info(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(l.w, "[gramaton-hook] %s %s: %s\n", ts, l.hookTag, msg)
}

// Close flushes and closes the log file. Safe on a nil logger.
func (l *Logger) Close() {
	if l == nil || l.w == nil {
		return
	}
	_ = l.w.Close()
}

// nopWriteCloser is used when the log file can't be opened; hooks
// should never fail to run because the log is unreachable.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// RunGramaton execs the gramaton binary with the given args and
// returns its combined stdout+stderr. Overridable for tests —
// assign a different function to exercise handlers without a
// real gramaton binary on PATH.
var RunGramaton = realRunGramaton

// realRunGramaton resolves the binary via $GRAMATON_BIN or exec.LookPath
// ("gramaton") and execs it. Default value of RunGramaton.
func realRunGramaton(args ...string) (string, error) {
	bin := os.Getenv("GRAMATON_BIN")
	if bin == "" {
		bin = "gramaton"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("gramaton binary not found on PATH (set GRAMATON_BIN to override): %w", err)
	}
	cmd := exec.Command(resolved, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// atomicWriteFile writes data to path via a .tmp file + rename.
// Prevents torn writes if the process is killed mid-flush.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		// Non-fatal on Windows (ignored); log-worthy on Unix but
		// the rename proceeds either way.
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// WriteJSON marshals v with stable key ordering and writes via
// atomicWriteFile.
func WriteJSON(path string, v any, perm os.FileMode) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return atomicWriteFile(path, data, perm)
}
