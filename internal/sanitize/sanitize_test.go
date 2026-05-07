package sanitize

import (
	"strings"
	"testing"
)

// TestFieldObservedPattern uses the exact corruption
// pattern observed on 2026-04-24 in two production records.
// Regression test: if this ever fails to strip cleanly, those
// records (or new ones like them) are back in the store with
// polluted embeddings.
func TestFieldObservedPattern(t *testing.T) {
	input := `Setup-wizard language principles for Gramaton (2026-04-22): lead with user benefit not concept; one-sentence concept explanations; "skip for now" on every optional step. Applies to wizard, doctor, error messages, any future GUI.</summary_short>
<parameter name="keywords">["gramaton wizard language principles", "onboarding UX design"]`
	got := Field(input)
	if strings.Contains(got, "</summary_short>") {
		t.Errorf("Field left </summary_short> in output: %q", got)
	}
	if strings.Contains(got, "<parameter name=") {
		t.Errorf("Field left <parameter name= in output: %q", got)
	}
	if !strings.HasSuffix(got, "any future GUI.") {
		t.Errorf("Field truncated at wrong boundary; got tail %q", got[len(got)-30:])
	}
}

func TestFieldLeavesCleanUnchanged(t *testing.T) {
	input := "Gramaton pre-OSS decision 2026-04-22: target tech-capable users. Interactive wizard is a MUST."
	got := Field(input)
	if got != input {
		t.Errorf("Field mutated a clean string\n got:  %q\n want: %q", got, input)
	}
}

// TestFieldPreservesLegitAngleBrackets confirms the strip
// is narrow: records that legitimately discuss HTML, XML, or code
// with angle brackets must NOT be truncated. A React tutorial
// summary would be mangled by an overly-aggressive strip.
func TestFieldPreservesLegitAngleBrackets(t *testing.T) {
	input := "React's <Button> and <Input> components use the <Form> wrapper; this differs from Vue's <template>-based approach."
	got := Field(input)
	if got != input {
		t.Errorf("Field stripped legitimate angle-bracket content\n got:  %q\n want: %q", got, input)
	}
}

func TestFieldTrimsWhitespace(t *testing.T) {
	input := "  \n\t Real summary here.  \n  "
	got := Field(input)
	want := "Real summary here."
	if got != want {
		t.Errorf("Field trim: got %q, want %q", got, want)
	}
}

func TestFieldEmptyInput(t *testing.T) {
	if got := Field(""); got != "" {
		t.Errorf("Field(\"\") = %q, want empty", got)
	}
}

func TestFieldAllBadTokens(t *testing.T) {
	// Each token in the strip list should land us at the prose-before
	// boundary. Protects against partial list regressions where a
	// pattern was added to badTailTokens but wasn't wired into
	// stripBadTail.
	tokens := []string{
		"</summary_short>",
		"</content>",
		"</keywords>",
		"</context_about>",
		"</parameter>",
		"<parameter name=\"other\">",
		"<|im_end|>",
		"<|endoftext|>",
	}
	for _, tok := range tokens {
		prose := "Good summary text that ends here."
		input := prose + tok + "trailing garbage"
		got := Field(input)
		if got != prose {
			t.Errorf("Field with token %q: got %q, want %q", tok, got, prose)
		}
	}
}

func TestValidateRejectsPureContamination(t *testing.T) {
	// Input was non-empty but sanitization left nothing — the field
	// was entirely tool-use-format garbage. Must reject so the caller
	// doesn't see a silently-emptied record.
	original := "</summary_short>\n<parameter name=\"keywords\">[]"
	sanitized := Field(original)
	if sanitized != "" {
		t.Fatalf("precondition: expected sanitized to be empty, got %q", sanitized)
	}
	err := Validate(original, sanitized, "summary_short", 1000)
	if err == nil {
		t.Fatal("Validate accepted pure-contamination input; want error")
	}
	if !strings.Contains(err.Error(), "structured-output") {
		t.Errorf("error message doesn't mention the cause: %v", err)
	}
}

func TestValidateAcceptsEmptyOriginal(t *testing.T) {
	// An empty field is fine (optional). Only the "had content,
	// sanitized to nothing" case is an error.
	if err := Validate("", "", "summary_short", 1000); err != nil {
		t.Errorf("Validate on empty input returned error: %v", err)
	}
}

func TestValidateRejectsOverlength(t *testing.T) {
	s := strings.Repeat("a", 2000)
	err := Validate(s, s, "summary_short", 1000)
	if err == nil {
		t.Fatal("Validate accepted over-length input; want error")
	}
	if !strings.Contains(err.Error(), "exceeds maximum length") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateAcceptsInBounds(t *testing.T) {
	s := "Normal-length summary within the 1000-char cap."
	if err := Validate(s, s, "summary_short", 1000); err != nil {
		t.Errorf("Validate on valid input returned error: %v", err)
	}
}
