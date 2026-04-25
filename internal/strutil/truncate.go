// Package strutil holds tiny string helpers shared across the LLM
// providers and similar low-level callers. Keep additions narrow:
// anything more than a one-screen helper belongs in its own package.
package strutil

import "unicode/utf8"

// Truncate returns s shortened to at most n bytes, appending "..." when
// the input was actually trimmed. Counts bytes, not runes -- callers
// passing UTF-8 risk splitting a multi-byte character at the boundary.
// Use TruncateRunes when the cap is in characters, not bytes.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TruncateRunes returns s shortened to at most maxRunes runes. No
// ellipsis. UTF-8 boundaries are respected -- multi-byte characters
// are never split. Use this when the cap is in user-visible
// characters rather than storage bytes.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// TrimToValidUTF8 trims trailing bytes from s until the result ends on
// a complete UTF-8 rune. Used when an upstream byte-cap may have cut
// a multi-byte character mid-rune. Returns s unchanged if it already
// ends cleanly.
//
// utf8.DecodeLastRuneInString returns size=1 for any RuneError --
// whether the trailing bytes are a dangling multi-byte sequence or
// canonical bad bytes -- so the loop trims byte-by-byte until either
// the tail is a complete rune or the string is empty.
func TrimToValidUTF8(s string) string {
	for len(s) > 0 {
		r, _ := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}
