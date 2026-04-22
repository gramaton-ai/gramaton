package setup

import (
	"errors"
	"testing"
)

// Tests for ScriptedPrompter, which is itself a test helper -- a
// broken ScriptedPrompter silently undermines every other test in
// the package, so it gets first-class coverage.

func TestScriptedPrompterText(t *testing.T) {
	p := NewScriptedPrompter("hello", "", "world")

	got, err := p.Text("")
	if err != nil || got != "hello" {
		t.Errorf("first Text: got (%q, %v), want (hello, nil)", got, err)
	}

	// Empty answer with a default should return the default.
	got, err = p.Text("fallback")
	if err != nil || got != "fallback" {
		t.Errorf("default fallback: got (%q, %v), want (fallback, nil)", got, err)
	}

	got, err = p.Text("")
	if err != nil || got != "world" {
		t.Errorf("third Text: got (%q, %v), want (world, nil)", got, err)
	}
}

func TestScriptedPrompterOutOfAnswers(t *testing.T) {
	// Out-of-answers must fail loudly (ErrAborted), not hang or
	// return zero values that tests might miss.
	p := NewScriptedPrompter("only one")

	if _, err := p.Text(""); err != nil {
		t.Fatalf("first read: unexpected error %v", err)
	}

	_, err := p.Text("")
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("second read: want ErrAborted, got %v", err)
	}
}

func TestScriptedPrompterChoice(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		max     int
		def     int
		wantIdx int
		wantErr bool
	}{
		{"valid first option", "1", 3, -1, 0, false},
		{"valid last option", "3", 3, -1, 2, false},
		{"out of range high", "4", 3, -1, 0, true},
		{"out of range low", "0", 3, -1, 0, true},
		{"not a number", "abc", 3, -1, 0, true},
		{"empty uses default", "", 3, 1, 1, false},
		{"empty without default errors", "", 3, -1, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewScriptedPrompter(tt.answer)
			got, err := p.Choice(tt.max, tt.def)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && got != tt.wantIdx {
				t.Errorf("got idx = %d, want %d", got, tt.wantIdx)
			}
		})
	}
}

func TestScriptedPrompterYesNo(t *testing.T) {
	tests := []struct {
		name       string
		answer     string
		defaultYes bool
		want       bool
		wantErr    bool
	}{
		{"y", "y", false, true, false},
		{"Y", "Y", false, true, false},
		{"yes", "yes", false, true, false},
		{"YES", "YES", false, true, false},
		{"n", "n", true, false, false},
		{"no", "no", true, false, false},
		{"empty with default yes", "", true, true, false},
		{"empty with default no", "", false, false, false},
		{"invalid input errors", "maybe", true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewScriptedPrompter(tt.answer)
			got, err := p.YesNo(tt.defaultYes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScriptedPrompterSecret(t *testing.T) {
	// Secret has no default fallback -- every call consumes an
	// answer. Blank answer means "user pressed Enter without
	// typing", which is a valid "skip" signal the caller
	// interprets.
	p := NewScriptedPrompter("sk-ant-real-key", "")

	got, err := p.Secret()
	if err != nil || got != "sk-ant-real-key" {
		t.Errorf("first Secret: got (%q, %v), want (sk-ant-real-key, nil)", got, err)
	}

	got, err = p.Secret()
	if err != nil || got != "" {
		t.Errorf("skipped Secret: got (%q, %v), want ('', nil)", got, err)
	}
}
