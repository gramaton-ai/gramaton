package setup

import (
	"bytes"
	"strings"
	"testing"
)

// Output tests assert the exact shape of what the wizard prints,
// because the wizard's UX IS the output. A refactor that accidentally
// drops a newline or swaps a checkmark prefix will change the user
// experience; we want those changes to surface here first.

func TestStepHeaderFormat(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.StepHeader(2, 4, "Autonomous curation")

	got := buf.String()
	// Blank line before + after the heading, exact title format.
	want := "\nStep 2 of 4: Autonomous curation\n\n"
	if got != want {
		t.Errorf("StepHeader output:\ngot  %q\nwant %q", got, want)
	}
}

func TestCheckWarnError(t *testing.T) {
	cases := []struct {
		name    string
		call    func(w Writer)
		wantSub string
	}{
		{"check", func(w Writer) { w.Check("did the thing") }, "  ✓ did the thing"},
		{"warn", func(w Writer) { w.Warn("minor issue") }, "  ⚠ minor issue"},
		{"error", func(w Writer) { w.ErrorLine("fatal") }, "  ✗ fatal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			tc.call(w)
			if !strings.Contains(buf.String(), tc.wantSub) {
				t.Errorf("want substring %q in %q", tc.wantSub, buf.String())
			}
		})
	}
}

func TestParagraphIndent(t *testing.T) {
	// Paragraph is responsible for indenting prose so it sits flush
	// with check/warn/error lines. A regression here would leave
	// prose un-indented and visually disjoint.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Paragraph("first line", "second line\nwith embedded newline")

	got := buf.String()
	wantLines := []string{
		"  first line",
		"  second line",
		"  with embedded newline",
	}
	for _, wl := range wantLines {
		if !strings.Contains(got, wl+"\n") {
			t.Errorf("missing line %q in:\n%s", wl, got)
		}
	}
}

func TestPromptNoTrailingNewline(t *testing.T) {
	// Prompt must not end with a newline; input sits on the same
	// visible line. This is a common accidental regression.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Prompt(">")

	got := buf.String()
	if strings.HasSuffix(got, "\n") {
		t.Errorf("Prompt should not end with newline, got %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1_000, "1.0 KB"},
		{130_000_000, "130.0 MB"},
		{1_500_000_000, "1.5 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
