// Package strutil holds tiny string helpers shared across the LLM
// providers and similar low-level callers. Keep additions narrow:
// anything more than a one-screen helper belongs in its own package.
package strutil

// Truncate returns s shortened to at most n bytes, appending "..." when
// the input was actually trimmed. Counts bytes, not runes -- callers
// passing UTF-8 risk splitting a multi-byte character at the boundary.
// (T-08 will revisit rune-safety as part of the broader UTF-8
// helpers pass.)
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
