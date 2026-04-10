package claudecli

import "testing"

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

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Fatal("short string should not be truncated")
	}
	if truncate("hello world", 5) != "hello..." {
		t.Fatal("long string should be truncated")
	}
}
