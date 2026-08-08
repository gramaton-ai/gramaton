package bert

import (
	"fmt"
	"strings"
	"testing"
)

// buildTestVocab creates a minimal BERT vocabulary for testing.
// Contains special tokens + enough real tokens to test WordPiece.
func buildTestVocab() string {
	tokens := []string{
		"[PAD]",   // 0
		"[UNK]",   // 1
		"[CLS]",   // 2
		"[SEP]",   // 3
		"[MASK]",  // 4
		"hello",   // 5
		"world",   // 6
		"the",     // 7
		"quick",   // 8
		"brown",   // 9
		"fox",     // 10
		"##s",     // 11
		"##ed",    // 12
		"##ing",   // 13
		"jump",    // 14
		"##er",    // 15
		"un",      // 16
		"##known", // 17
		"a",       // 18
		"test",    // 19
		",",       // 20
		".",       // 21
		"!",       // 22
	}
	return strings.Join(tokens, "\n") + "\n"
}

func buildTestTokenizerJSON() string {
	return `{
		"model": {
			"type": "WordPiece",
			"vocab": {
				"[PAD]": 0, "[UNK]": 1, "[CLS]": 2, "[SEP]": 3, "[MASK]": 4,
				"hello": 5, "world": 6, "the": 7, "quick": 8, "brown": 9,
				"fox": 10, "##s": 11, "##ed": 12, "##ing": 13, "jump": 14,
				"##er": 15, "un": 16, "##known": 17, "a": 18, "test": 19,
				",": 20, ".": 21, "!": 22
			}
		},
		"added_tokens": [
			{"id": 0, "content": "[PAD]"},
			{"id": 1, "content": "[UNK]"},
			{"id": 2, "content": "[CLS]"},
			{"id": 3, "content": "[SEP]"}
		],
		"normalizer": {
			"type": "BertNormalizer",
			"lowercase": true,
			"strip_accents": true
		},
		"truncation": {
			"max_length": 512
		}
	}`
}

func TestTokenizerFromVocab(t *testing.T) {
	tok, err := newTokenizerFromVocab([]byte(buildTestVocab()))
	if err != nil {
		t.Fatal(err)
	}
	if tok.VocabSize() != 23 {
		t.Errorf("vocab size: got %d, want 23", tok.VocabSize())
	}
	if tok.MaxLen() != 512 {
		t.Errorf("max len: got %d, want 512", tok.MaxLen())
	}
}

func TestTokenizerFromJSON(t *testing.T) {
	tok, err := NewTokenizerFromJSON([]byte(buildTestTokenizerJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if tok.VocabSize() != 23 {
		t.Errorf("vocab size: got %d, want 23", tok.VocabSize())
	}
}

func TestTokenizerFromJSONBadType(t *testing.T) {
	data := `{"model":{"type":"BPE","vocab":{}}}`
	_, err := NewTokenizerFromJSON([]byte(data))
	if err == nil {
		t.Error("expected error for non-WordPiece model")
	}
}

func TestEncodeBasic(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, mask, types := tok.Encode("hello world")

	// Expected: [CLS] hello world [SEP]
	wantIDs := []int32{2, 5, 6, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids length: got %d, want %d", len(ids), len(wantIDs))
	}
	for i, id := range ids {
		if id != wantIDs[i] {
			t.Errorf("ids[%d]: got %d, want %d", i, id, wantIDs[i])
		}
	}

	// Mask should be all 1s.
	for i, m := range mask {
		if m != 1 {
			t.Errorf("mask[%d]: got %d, want 1", i, m)
		}
	}

	// Types should be all 0s.
	for i, ty := range types {
		if ty != 0 {
			t.Errorf("types[%d]: got %d, want 0", i, ty)
		}
	}
}

func TestEncodePunctuation(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("hello, world!")

	// Expected: [CLS] hello , world ! [SEP]
	wantIDs := []int32{2, 5, 20, 6, 22, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids length: got %d (%v), want %d", len(ids), ids, len(wantIDs))
	}
	for i, id := range ids {
		if id != wantIDs[i] {
			t.Errorf("ids[%d]: got %d, want %d", i, id, wantIDs[i])
		}
	}
}

func TestEncodeWordPiece(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("unknown")

	// "unknown" -> "un" + "##known"
	// Expected: [CLS] un ##known [SEP]
	wantIDs := []int32{2, 16, 17, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids length: got %d (%v), want %d", len(ids), ids, len(wantIDs))
	}
	for i, id := range ids {
		if id != wantIDs[i] {
			t.Errorf("ids[%d]: got %d, want %d", i, id, wantIDs[i])
		}
	}
}

func TestEncodeWordPieceSuffix(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("jumps")

	// "jumps" -> "jump" + "##s"
	// Expected: [CLS] jump ##s [SEP]
	wantIDs := []int32{2, 14, 11, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids length: got %d (%v), want %d", len(ids), ids, len(wantIDs))
	}
	for i, id := range ids {
		if id != wantIDs[i] {
			t.Errorf("ids[%d]: got %d, want %d", i, id, wantIDs[i])
		}
	}
}

func TestEncodeUnknownWord(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("xyz")

	// "xyz" not in vocab, each character not in vocab -> [UNK] per char
	// Expected: [CLS] [UNK] [UNK] [UNK] [SEP]
	if ids[0] != 2 || ids[len(ids)-1] != 3 {
		t.Errorf("expected [CLS] and [SEP] wrapper, got %v", ids)
	}
	for _, id := range ids[1 : len(ids)-1] {
		if id != 1 { // [UNK]
			t.Errorf("expected [UNK] (1), got %d", id)
		}
	}
}

func TestEncodeLowercase(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("Hello World")

	// Lowercased: "hello world"
	// Expected: [CLS] hello world [SEP]
	wantIDs := []int32{2, 5, 6, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids length: got %d (%v), want %d", len(ids), ids, len(wantIDs))
	}
	for i, id := range ids {
		if id != wantIDs[i] {
			t.Errorf("ids[%d]: got %d, want %d", i, id, wantIDs[i])
		}
	}
}

func TestEncodeEmpty(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, mask, types := tok.Encode("")

	// Expected: [CLS] [SEP]
	wantIDs := []int32{2, 3}
	if len(ids) != 2 {
		t.Fatalf("ids: got %v, want %v", ids, wantIDs)
	}
	if ids[0] != 2 || ids[1] != 3 {
		t.Errorf("ids: got %v, want %v", ids, wantIDs)
	}
	if len(mask) != 2 || mask[0] != 1 || mask[1] != 1 {
		t.Errorf("mask: got %v", mask)
	}
	if len(types) != 2 {
		t.Errorf("types length: got %d", len(types))
	}
}

func TestEncodeTruncation(t *testing.T) {
	// Build tokenizer with max_length=6 (including [CLS] and [SEP]).
	tok, _ := NewTokenizerFromJSON([]byte(`{
		"model": {
			"type": "WordPiece",
			"vocab": {
				"[PAD]": 0, "[UNK]": 1, "[CLS]": 2, "[SEP]": 3,
				"a": 4, "b": 5, "c": 6, "d": 7, "e": 8
			}
		},
		"added_tokens": [],
		"truncation": {"max_length": 6}
	}`))

	ids, _, _ := tok.Encode("a b c d e")

	// max_length=6: [CLS] a b c d [SEP] (e is truncated)
	if len(ids) != 6 {
		t.Fatalf("expected 6 tokens, got %d: %v", len(ids), ids)
	}
	if ids[0] != 2 || ids[5] != 3 {
		t.Errorf("expected [CLS]...[SEP], got %v", ids)
	}
}

func TestEncodeMultipleSpaces(t *testing.T) {
	tok, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	ids, _, _ := tok.Encode("hello   world")

	// Multiple spaces should collapse.
	wantIDs := []int32{2, 5, 6, 3}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids: got %v, want %v", ids, wantIDs)
	}
}

func TestPretokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"hello, world!", []string{"hello", ",", "world", "!"}},
		{"  spaces  ", []string{"spaces"}},
		{"a.b.c", []string{"a", ".", "b", ".", "c"}},
		{"don't", []string{"don", "'", "t"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := pretokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("pretokenize(%q): got %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("pretokenize(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestStripAccents(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},                   // no change
		{"cafe\u0301", "cafe"},               // combining accent stripped (é decomposed)
		{"ASCII only 123", "ASCII only 123"}, // fast path
	}

	for _, tt := range tests {
		got := stripAccents(tt.input)
		if got != tt.want {
			t.Errorf("stripAccents(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsPunct(t *testing.T) {
	puncts := []rune{'.', ',', '!', '?', ';', ':', '"', '\'', '(', ')', '[', ']', '{', '}', '-', '+', '=', '@', '#', '$', '%', '&', '*', '/', '\\', '|', '~', '^', '<', '>'}
	for _, r := range puncts {
		if !isPunct(r) {
			t.Errorf("isPunct(%q): got false, want true", r)
		}
	}

	nonPuncts := []rune{'a', 'Z', '0', '9', '_', ' ', '\t'}
	for _, r := range nonPuncts {
		if isPunct(r) {
			t.Errorf("isPunct(%q): got true, want false", r)
		}
	}
}

// TestValidateSpecialTokensRejectsMissingPAD covers the case where a
// vocab missing [PAD] previously slipped through validation, leaving
// id("[PAD]") to silently return 0 and collide with whatever real
// token owned ID 0.
func TestValidateSpecialTokensRejectsMissingPAD(t *testing.T) {
	required := []string{"[CLS]", "[SEP]", "[UNK]", "[PAD]"}
	for _, missing := range required {
		t.Run("missing_"+missing, func(t *testing.T) {
			vocab := map[string]int32{
				"[CLS]": 0, "[SEP]": 1, "[UNK]": 2, "[PAD]": 3, "hello": 4,
			}
			delete(vocab, missing)
			tok := &Tokenizer{vocab: vocab}
			if err := tok.validateSpecialTokens(); err == nil {
				t.Errorf("expected error for missing %s, got nil", missing)
			}
		})
	}
}

// TestSetMaxLen covers the clamp path used by Provider to enforce
// tokenizer.maxLen <= model.MaxPositionEmbeds.
func TestSetMaxLen(t *testing.T) {
	tok, err := NewTokenizerFromJSON([]byte(buildTestTokenizerJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if tok.MaxLen() != 512 {
		t.Fatalf("expected default 512, got %d", tok.MaxLen())
	}
	tok.SetMaxLen(128)
	if tok.MaxLen() != 128 {
		t.Errorf("expected 128 after SetMaxLen, got %d", tok.MaxLen())
	}
	// Zero or negative is ignored (defensive).
	tok.SetMaxLen(0)
	if tok.MaxLen() != 128 {
		t.Errorf("SetMaxLen(0) should be ignored, got %d", tok.MaxLen())
	}
}

func TestTokenizerFromJSONMatchesVocab(t *testing.T) {
	// Ensure both loading methods produce the same tokenization.
	tokJSON, _ := NewTokenizerFromJSON([]byte(buildTestTokenizerJSON()))
	tokVocab, _ := newTokenizerFromVocab([]byte(buildTestVocab()))

	texts := []string{
		"hello world",
		"the quick brown fox jumps",
		"unknown",
		"Hello, World!",
		"",
	}

	for _, text := range texts {
		idsJSON, _, _ := tokJSON.Encode(text)
		idsVocab, _, _ := tokVocab.Encode(text)

		if len(idsJSON) != len(idsVocab) {
			t.Errorf("Encode(%q): JSON=%v, Vocab=%v", text, idsJSON, idsVocab)
			continue
		}
		for i := range idsJSON {
			if idsJSON[i] != idsVocab[i] {
				t.Errorf("Encode(%q)[%d]: JSON=%d, Vocab=%d", text, i, idsJSON[i], idsVocab[i])
			}
		}
	}
}

// newTokenizerFromVocab parses a plain vocab.txt file (one token per line,
// indexed by line number). Uses default BERT normalizer settings.
func newTokenizerFromVocab(data []byte) (*Tokenizer, error) {
	lines := strings.Split(string(data), "\n")
	vocab := make(map[string]int32, len(lines))
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue // trailing newline
		}
		vocab[line] = int32(i)
	}
	if len(vocab) == 0 {
		return nil, fmt.Errorf("tokenizer: empty vocab")
	}

	t := &Tokenizer{
		vocab:    vocab,
		maxLen:   DefaultMaxLen,
		doLower:  true,
		stripAcc: true,
	}
	t.unkID = t.id("[UNK]")
	t.clsID = t.id("[CLS]")
	t.sepID = t.id("[SEP]")
	t.padID = t.id("[PAD]")

	if err := t.validateSpecialTokens(); err != nil {
		return nil, err
	}
	return t, nil
}
