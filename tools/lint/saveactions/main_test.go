package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePath builds the in-tree path used to materialize a fixture.
// The fixture itself is .go.txt; copy it to a temp dir as .go before
// linting so go/parser sees a real .go file.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name+".go.txt")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, name+".go")
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", dst, err)
	}
	return dst
}

// TestLintCleanFixture exercises the happy path: every Save call in
// the clean fixture passes at least one CommitAction. Lint must
// produce zero findings.
func TestLintCleanFixture(t *testing.T) {
	path := fixturePath(t, "clean")
	got, err := lintFile(path)
	if err != nil {
		t.Fatalf("lintFile: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected zero findings on clean fixture, got %d:\n%v", len(got), got)
	}
}

// TestLintDirtyFixture exercises the violation path: two Save calls
// have no action; one is pragma-exempted. Lint must report exactly
// the two violations and skip the exempted one.
func TestLintDirtyFixture(t *testing.T) {
	path := fixturePath(t, "dirty")
	got, err := lintFile(path)
	if err != nil {
		t.Fatalf("lintFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 findings on dirty fixture, got %d:\n%v", len(got), got)
	}
	// Both findings reference Save methods. Spot-check the message
	// shape so a regression in formatting surfaces here.
	for _, f := range got {
		if !strings.Contains(f.msg, "no CommitAction") {
			t.Errorf("finding message missing expected fragment: %q", f.msg)
		}
		if !strings.Contains(f.msg, pragmaExempt) {
			t.Errorf("finding message should reference the exemption pragma; got: %q", f.msg)
		}
	}
}

// TestLintIgnoresTestFiles ensures the file-walk skips *_test.go.
// Mirrors the production rule that test files legitimately call
// Save() without actions for fixture setup.
func TestLintIgnoresTestFiles(t *testing.T) {
	dir := t.TempDir()
	body, err := os.ReadFile(filepath.Join("testdata", "dirty.go.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Drop the dirty fixture into a *_test.go file -- walk should
	// skip it entirely so no findings come back.
	dst := filepath.Join(dir, "fake_test.go")
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := walk(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected walk to skip *_test.go, got %d findings:\n%v", len(got), got)
	}
}

// TestLintIgnoresTestutilDir ensures the file-walk skips testutil/
// subtrees. Mirrors the convention that testutil packages hold
// test-only helpers and don't need per-call exemption pragmas.
func TestLintIgnoresTestutilDir(t *testing.T) {
	dir := t.TempDir()
	tu := filepath.Join(dir, "testutil")
	if err := os.MkdirAll(tu, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", "dirty.go.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := filepath.Join(tu, "helper.go")
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := walk(dir)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected walk to skip testutil/, got %d findings:\n%v", len(got), got)
	}
}
