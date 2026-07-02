package cli

import (
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/setup"
)

// TestParseAuthor pins the --author parsing rules: git-style
// "Name <email>" splits at the LAST '<' with a required trailing
// '>'; a value without '<' is all name; a '<' without a closing '>'
// is a user-facing error.
func TestParseAuthor(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    config.AuthorConfig
		wantErr bool
	}{
		{
			name:  "full form",
			input: "Ada Lovelace <ada@example.com>",
			want:  config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"},
		},
		{
			name:  "name only",
			input: "Ada Lovelace",
			want:  config.AuthorConfig{Name: "Ada Lovelace"},
		},
		{
			name:  "email only",
			input: "<ada@example.com>",
			want:  config.AuthorConfig{Email: "ada@example.com"},
		},
		{
			name:  "whitespace trimmed",
			input: "  Ada Lovelace   < ada@example.com > ",
			want:  config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"},
		},
		{
			name:  "last angle bracket wins",
			input: "Ada <the Countess> Lovelace <ada@example.com>",
			want:  config.AuthorConfig{Name: "Ada <the Countess> Lovelace", Email: "ada@example.com"},
		},
		{
			name:    "malformed: missing closing bracket",
			input:   "Ada Lovelace <ada@example.com",
			wantErr: true,
		},
		{
			name:    "malformed: text after closing bracket",
			input:   "Ada Lovelace <ada@example.com> trailing",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAuthor(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAuthor(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("parseAuthor(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

// TestDefaultAuthorFallback covers the defaultAuthor helper plus the
// config.Save serialization it feeds: a blank cfg.Author gets Name
// from the OS account (same preference order as the wizard) with
// Email left blank, a cfg.Author already populated from --author is
// not touched, and the resulting Author survives a Save/Load
// round-trip. It does NOT drive runInitNonInteractive itself -- that
// function's embedding detection (embed.SetupEmbedding) probes
// providers and may download models, so there is no hermetic way to
// run it. The uncovered wiring is one line: runInitNonInteractive
// calls defaultAuthor(&cfg) immediately before the same config.Save
// exercised here.
func TestDefaultAuthorFallback(t *testing.T) {
	cfg := config.Defaults()
	defaultAuthor(&cfg)
	if want := setup.OSAccountName(); cfg.Author.Name != want {
		t.Errorf("Author.Name = %q, want OS account %q", cfg.Author.Name, want)
	}
	if cfg.Author.Email != "" {
		t.Errorf("Author.Email = %q, want empty", cfg.Author.Email)
	}

	// Round-trip through the same Save the non-interactive path uses.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Author != cfg.Author {
		t.Errorf("saved Author = %+v, want %+v", loaded.Author, cfg.Author)
	}

	// --author preset wins over the fallback.
	preset := config.Defaults()
	preset.Author = config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"}
	defaultAuthor(&preset)
	if preset.Author.Name != "Ada Lovelace" || preset.Author.Email != "ada@example.com" {
		t.Errorf("preset Author overwritten by fallback: %+v", preset.Author)
	}
}
