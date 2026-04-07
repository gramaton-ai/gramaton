package index

import (
	"math"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"simple", "hello world", []string{"hello", "world"}},
		{"mixed case", "Hello World", []string{"hello", "world"}},
		{"punctuation", "hello, world! foo-bar", []string{"hello", "world", "foo", "bar"}},
		{"single chars dropped", "a b cd ef", []string{"cd", "ef"}},
		{"numbers kept", "test123 foo42", []string{"test123", "foo42"}},
		{"unicode", "Bewusstsein Gedaechtnis", []string{"bewusstsein", "gedaechtnis"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Tokenize(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("Tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("token[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"both empty", nil, nil, 0},
		{"one empty", []string{"foo"}, nil, 0},
		{"identical", []string{"foo", "bar"}, []string{"foo", "bar"}, 1.0},
		{"no overlap", []string{"foo", "bar"}, []string{"baz", "qux"}, 0},
		{"partial overlap", []string{"foo", "bar", "baz"}, []string{"bar", "baz", "qux"}, 0.5},
		{"duplicates in input", []string{"foo", "foo", "bar"}, []string{"foo", "bar"}, 1.0},
		{"subset", []string{"foo", "bar"}, []string{"foo", "bar", "baz"}, 2.0 / 3.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JaccardSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("JaccardSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestJaccardOnRealContent(t *testing.T) {
	// Simulate the false positive scenario: two academic articles with
	// similar structure but different content.
	articleA := `Functionalism is a theory about the nature of mental states.
		According to functionalism, mental states are identified by what they do
		rather than by what they are made of. This theory is the most widely
		accepted theory of mind in modern philosophy. Memory trace decay is
		defined by its functional role in cognitive systems.`

	articleB := `Time is one of the most fundamental concepts in physics and
		philosophy. The nature of time has been debated for millennia. Some
		philosophers argue that time is an illusion, while others maintain
		that it is a fundamental feature of the universe. Temporal experience
		shapes our understanding of causation and change.`

	tokA := Tokenize(articleA)
	tokB := Tokenize(articleB)

	sim := JaccardSimilarity(tokA, tokB)
	// These articles share common words (is, of, the, etc.) but have
	// very different content. Jaccard should be well below 0.5.
	if sim >= 0.5 {
		t.Errorf("structurally different articles have Jaccard %f, expected < 0.5", sim)
	}

	// Same article should be 1.0.
	if got := JaccardSimilarity(tokA, tokA); got != 1.0 {
		t.Errorf("same article Jaccard = %f, want 1.0", got)
	}

	// Slightly modified version should be high.
	modified := strings.Replace(articleA, "Memory trace decay", "Neural trace decay", 1)
	tokMod := Tokenize(modified)
	modSim := JaccardSimilarity(tokA, tokMod)
	if modSim < 0.8 {
		t.Errorf("slightly modified article Jaccard = %f, expected > 0.8", modSim)
	}
}
