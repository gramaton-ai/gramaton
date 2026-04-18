package claudecli

import (
	"testing"

	"github.com/gramaton-ai/gramaton/internal/strutil"
)

func TestModelAliases(t *testing.T) {
	if modelAliases["haiku"] != "haiku" {
		t.Fatal("haiku alias wrong")
	}
	if modelAliases["sonnet"] != "sonnet" {
		t.Fatal("sonnet alias wrong")
	}
	if modelAliases["opus"] != "opus" {
		t.Fatal("opus alias wrong")
	}
}

func TestTruncateRedirectedToStrutil(t *testing.T) {
	if strutil.Truncate("hello", 10) != "hello" {
		t.Fatal("short string should not be truncated")
	}
	if strutil.Truncate("hello world", 5) != "hello..." {
		t.Fatal("long string should be truncated")
	}
}
