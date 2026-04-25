package strutil

import "testing"

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hello..."},
		{"hello world", 8, "hello wo..."},
		{"", 8, ""},
	}
	for _, tc := range cases {
		got := Truncate(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestTruncateRunesASCII(t *testing.T) {
	cases := []struct {
		in       string
		maxRunes int
		want     string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"", 5, ""},
	}
	for _, tc := range cases {
		got := TruncateRunes(tc.in, tc.maxRunes)
		if got != tc.want {
			t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
		}
	}
}

// TestTruncateRunesMultibyte pins the contract that multi-byte runes
// are never split mid-character. The naive byte-truncation approach
// (s[:n]) would corrupt these inputs.
func TestTruncateRunesMultibyte(t *testing.T) {
	cases := []struct {
		in       string
		maxRunes int
		want     string
	}{
		// 3-byte runes (CJK).
		{"日本語", 2, "日本"},
		{"日本語", 3, "日本語"},
		{"日本語", 1, "日"},
		// 4-byte runes (emoji).
		{"a😀b", 2, "a😀"},
		{"a😀b", 1, "a"},
		// Mixed.
		{"café", 3, "caf"},
		{"café", 4, "café"},
	}
	for _, tc := range cases {
		got := TruncateRunes(tc.in, tc.maxRunes)
		if got != tc.want {
			t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
		}
	}
}

func TestTrimToValidUTF8(t *testing.T) {
	// "café" is c(1) a(1) f(1) é(2). Bytes: 63 61 66 c3 a9.
	full := "café"
	// Remove the trailing 0xa9 continuation byte -- leaves a dangling 0xc3.
	bad := full[:len(full)-1]
	got := TrimToValidUTF8(bad)
	if got != "caf" {
		t.Errorf("TrimToValidUTF8(%q) = %q, want %q", bad, got, "caf")
	}

	// A clean string is unchanged.
	if TrimToValidUTF8(full) != full {
		t.Errorf("clean input mutated: got %q", TrimToValidUTF8(full))
	}

	// All-bad bytes get trimmed to empty.
	if got := TrimToValidUTF8("\xc3\xc3\xc3"); got != "" {
		t.Errorf("all-bad input: got %q, want empty", got)
	}

	// Empty input.
	if got := TrimToValidUTF8(""); got != "" {
		t.Errorf("empty input: got %q", got)
	}
}
