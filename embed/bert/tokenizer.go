package bert

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gramaton-ai/gramaton/internal/strutil"
)

// Tokenizer implements BERT's WordPiece tokenization pipeline.
// Supports loading from HuggingFace tokenizer.json or plain vocab.txt.
type Tokenizer struct {
	vocab    map[string]int32
	unkID    int32
	clsID    int32
	sepID    int32
	padID    int32
	maxLen   int
	doLower  bool
	stripAcc bool
}

// DefaultMaxLen is the default maximum sequence length for BERT models.
const DefaultMaxLen = 512

// NewTokenizerFromJSON parses a HuggingFace tokenizer.json file and
// returns a configured Tokenizer. This is the preferred loading method
// as it captures the model's exact normalizer and vocab settings.
func NewTokenizerFromJSON(data []byte) (*Tokenizer, error) {
	var tj tokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("tokenizer: parse json: %w", err)
	}

	if tj.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("tokenizer: unsupported model type %q, want WordPiece", tj.Model.Type)
	}

	vocab := make(map[string]int32, len(tj.Model.Vocab))
	for token, id := range tj.Model.Vocab {
		vocab[token] = int32(id)
	}

	// Also register added_tokens (like [CLS], [SEP], [PAD], [UNK]).
	for _, at := range tj.AddedTokens {
		vocab[at.Content] = int32(at.ID)
	}

	maxLen := DefaultMaxLen
	if tj.Truncation != nil && tj.Truncation.MaxLength > 0 {
		maxLen = tj.Truncation.MaxLength
	}

	doLower := false
	stripAcc := false
	if tj.Normalizer != nil {
		doLower = tj.Normalizer.Lowercase
		stripAcc = tj.Normalizer.StripAccents
	}

	t := &Tokenizer{
		vocab:    vocab,
		maxLen:   maxLen,
		doLower:  doLower,
		stripAcc: stripAcc,
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

// NewTokenizerFromVocab parses a plain vocab.txt file (one token per line,
// indexed by line number). Uses default BERT normalizer settings.
func NewTokenizerFromVocab(data []byte) (*Tokenizer, error) {
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

// Encode tokenizes a text string and returns input tensors for BERT.
// Returns token IDs, attention mask (1 for real tokens, 0 for padding),
// and token type IDs (all 0 for single-segment input).
// The output is truncated to maxLen and does NOT include padding --
// the caller can pad if needed for batching.
func (t *Tokenizer) Encode(text string) (ids, mask, types []int32) {
	// Early truncation: avoid expensive pretokenization of text that
	// will be discarded by the 512-token limit. 6 chars/token is
	// conservative (English averages ~4); this guarantees we never
	// miss tokens that would have fit in the window.
	maxChars := t.maxLen * 6
	if len(text) > maxChars {
		// Byte-cap, then trim back to a complete-rune boundary so the
		// downstream tokenizer never sees a partial multi-byte sequence.
		text = strutil.TrimToValidUTF8(text[:maxChars])
	}

	// Normalize.
	if t.doLower {
		text = strings.ToLower(text)
	}
	if t.stripAcc {
		text = stripAccents(text)
	}

	// Pre-tokenize: split on whitespace and punctuation.
	words := pretokenize(text)

	// WordPiece tokenization.
	tokens := make([]int32, 0, len(words)+2) // +2 for [CLS] and [SEP]
	tokens = append(tokens, t.clsID)

	maxWordTokens := t.maxLen - 2 // reserve space for [CLS] and [SEP]
	for _, word := range words {
		if len(tokens)-1 >= maxWordTokens {
			break
		}
		wordTokens := t.wordpiece(word)
		remaining := maxWordTokens - (len(tokens) - 1)
		if len(wordTokens) > remaining {
			wordTokens = wordTokens[:remaining]
		}
		tokens = append(tokens, wordTokens...)
	}

	tokens = append(tokens, t.sepID)

	// Build attention mask and token type IDs.
	n := len(tokens)
	mask = make([]int32, n)
	types = make([]int32, n)
	for i := 0; i < n; i++ {
		mask[i] = 1
		// types[i] = 0 (already zero-valued, single segment)
	}

	return tokens, mask, types
}

// VocabSize returns the number of tokens in the vocabulary.
func (t *Tokenizer) VocabSize() int {
	return len(t.vocab)
}

// MaxLen returns the maximum sequence length.
func (t *Tokenizer) MaxLen() int {
	return t.maxLen
}

// SetMaxLen overrides the tokenizer's maximum sequence length.
// Used by the provider to clamp tokenizer truncation to the model's
// MaxPositionEmbeds when tokenizer.json declares a larger value than
// the model can actually process. Without this clamp, model.Forward
// panics with a slice-bounds error on the scratch buffers.
func (t *Tokenizer) SetMaxLen(n int) {
	if n > 0 {
		t.maxLen = n
	}
}

// wordpiece applies the WordPiece algorithm to a single word.
// Returns a slice of token IDs. Unknown subwords produce [UNK].
func (t *Tokenizer) wordpiece(word string) []int32 {
	if _, ok := t.vocab[word]; ok {
		return []int32{t.vocab[word]}
	}

	var tokens []int32
	start := 0
	for start < len(word) {
		end := len(word)
		found := false
		for end > start {
			substr := word[start:end]
			if start > 0 {
				substr = "##" + substr
			}
			if id, ok := t.vocab[substr]; ok {
				tokens = append(tokens, id)
				start = end
				found = true
				break
			}
			// Move end back by one rune.
			_, size := utf8.DecodeLastRuneInString(word[start:end])
			end -= size
		}
		if !found {
			tokens = append(tokens, t.unkID)
			// Skip one rune.
			_, size := utf8.DecodeRuneInString(word[start:])
			start += size
		}
	}
	return tokens
}

func (t *Tokenizer) validateSpecialTokens() error {
	required := []string{"[CLS]", "[SEP]", "[UNK]", "[PAD]"}
	for _, tok := range required {
		if _, ok := t.vocab[tok]; !ok {
			return fmt.Errorf("tokenizer: missing required special token %s", tok)
		}
	}
	return nil
}

func (t *Tokenizer) id(token string) int32 {
	if id, ok := t.vocab[token]; ok {
		return id
	}
	return 0
}

// pretokenize splits text on whitespace and punctuation, matching
// BERT's BasicTokenizer behavior. Punctuation characters become
// individual tokens. Whitespace is consumed but not tokenized.
func pretokenize(text string) []string {
	var words []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			continue
		}
		if isPunct(r) {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			words = append(words, string(r))
			continue
		}
		// Skip control characters.
		if unicode.IsControl(r) {
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// isPunct matches BERT's definition of punctuation: Unicode Punct category
// plus ASCII symbols that aren't letters, digits, or underscore.
// Underscore is Unicode Pc (connector punctuation) but BERT treats it
// as a regular word character.
func isPunct(r rune) bool {
	if r == '_' {
		return false
	}
	if unicode.IsPunct(r) || unicode.IsSymbol(r) {
		return true
	}
	// ASCII punctuation not in Unicode Punct category.
	if r >= 33 && r <= 47 {
		return true // ! " # $ % & ' ( ) * + , - . /
	}
	if r >= 58 && r <= 64 {
		return true // : ; < = > ? @
	}
	if r >= 91 && r <= 96 && r != 95 {
		return true // [ \ ] ^ ` (not _)
	}
	if r >= 123 && r <= 126 {
		return true // { | } ~
	}
	return false
}

// stripAccents removes combining diacritical marks from text.
// Uses Unicode normalization: decomposes characters, then removes
// combining marks (category Mn).
func stripAccents(text string) string {
	// Fast path: no non-ASCII characters.
	ascii := true
	for _, r := range text {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		// Skip combining marks (Unicode category Mn).
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tokenizerJSON is the structure of a HuggingFace tokenizer.json file.
type tokenizerJSON struct {
	Model       tokenizerModel    `json:"model"`
	AddedTokens []addedToken      `json:"added_tokens"`
	Normalizer  *normalizerConfig `json:"normalizer"`
	Truncation  *truncationConfig `json:"truncation"`
}

type tokenizerModel struct {
	Type  string         `json:"type"`
	Vocab map[string]int `json:"vocab"`
}

type addedToken struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
}

type normalizerConfig struct {
	Lowercase    bool `json:"lowercase"`
	StripAccents bool `json:"strip_accents"`
}

type truncationConfig struct {
	MaxLength int `json:"max_length"`
}
