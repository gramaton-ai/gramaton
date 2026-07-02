package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := StoreManifest{
		ReadOnly:    true,
		Owner:       "ada@example.com",
		PublishedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteStoreManifest(dir, want); err != nil {
		t.Fatalf("WriteStoreManifest: %v", err)
	}

	got, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}
	if got.ReadOnly != want.ReadOnly || got.Owner != want.Owner || !got.PublishedAt.Equal(want.PublishedAt) {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}

	// Pin the on-disk JSON shape: the manifest travels with copies,
	// tars, and backups, so the serialization is a contract.
	data, err := os.ReadFile(filepath.Join(dir, "STORE"))
	if err != nil {
		t.Fatalf("read STORE file: %v", err)
	}
	body := string(data)
	for _, frag := range []string{
		`"readonly":true`,
		`"owner":"ada@example.com"`,
		`"published_at":"2026-07-01T12:00:00Z"`,
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("STORE body missing %s; got %s", frag, body)
		}
	}
}

// TestWriteStoreManifestOmitsEmptyProvenance pins the omitempty /
// omitzero behavior: a writable manifest with no provenance
// serializes to just the readonly flag.
func TestWriteStoreManifestOmitsEmptyProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStoreManifest(dir, StoreManifest{}); err != nil {
		t.Fatalf("WriteStoreManifest: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "STORE"))
	if err != nil {
		t.Fatalf("read STORE file: %v", err)
	}
	if got := string(data); got != `{"readonly":false}` {
		t.Fatalf("zero manifest body = %s, want {\"readonly\":false}", got)
	}
}

func TestReadStoreManifestAbsent(t *testing.T) {
	m, err := ReadStoreManifest(t.TempDir())
	if err != nil {
		t.Fatalf("absent manifest should return nil error, got %v", err)
	}
	if m.ReadOnly || m.Owner != "" || !m.PublishedAt.IsZero() {
		t.Fatalf("absent manifest should be the zero (writable) manifest, got %+v", m)
	}
}

func TestReadStoreManifestCorrupted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "STORE"), []byte("{readonly:"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadStoreManifest(dir)
	if err == nil {
		t.Fatal("corrupted manifest must fail loud, not silently read as writable")
	}
}

func TestFreezeStoreSetsFields(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UTC().Truncate(time.Second)
	if err := FreezeStore(dir, "ada@example.com"); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	m, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("ReadStoreManifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("freeze should set readonly=true")
	}
	if m.Owner != "ada@example.com" {
		t.Errorf("owner = %q, want ada@example.com", m.Owner)
	}
	if m.PublishedAt.Before(before) || m.PublishedAt.After(after) {
		t.Errorf("published_at %v outside freeze window [%v, %v]", m.PublishedAt, before, after)
	}
}

// TestFreezeStoreIdempotent verifies that freezing an already-frozen
// store is a no-op preserving the ORIGINAL owner and published_at --
// a second freeze must not rewrite the provenance of the original
// publication.
func TestFreezeStoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := FreezeStore(dir, "original"); err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	first, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read after first freeze: %v", err)
	}

	if err := FreezeStore(dir, "impostor"); err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	second, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read after second freeze: %v", err)
	}
	if !second.ReadOnly {
		t.Error("store should still be frozen")
	}
	if second.Owner != "original" {
		t.Errorf("owner = %q after re-freeze, want original preserved", second.Owner)
	}
	if !second.PublishedAt.Equal(first.PublishedAt) {
		t.Errorf("published_at changed on re-freeze: %v -> %v", first.PublishedAt, second.PublishedAt)
	}
}

// TestThawStorePreservesProvenance verifies thaw clears readonly but
// keeps owner and published_at: provenance of the original
// publication survives a thaw.
func TestThawStorePreservesProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := FreezeStore(dir, "ada@example.com"); err != nil {
		t.Fatalf("FreezeStore: %v", err)
	}
	frozen, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read frozen manifest: %v", err)
	}

	if err := ThawStore(dir); err != nil {
		t.Fatalf("ThawStore: %v", err)
	}
	m, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read thawed manifest: %v", err)
	}
	if m.ReadOnly {
		t.Error("thaw should clear readonly")
	}
	if m.Owner != "ada@example.com" {
		t.Errorf("owner = %q after thaw, want preserved", m.Owner)
	}
	if !m.PublishedAt.Equal(frozen.PublishedAt) {
		t.Errorf("published_at changed on thaw: %v -> %v", frozen.PublishedAt, m.PublishedAt)
	}
}

func TestThawStoreNoOpWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	if err := ThawStore(dir); err != nil {
		t.Fatalf("ThawStore on never-frozen store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "STORE")); !os.IsNotExist(err) {
		t.Fatal("thawing a never-frozen store must not create a manifest")
	}
}

// TestRefreezeAfterThawOverwritesProvenance verifies the freeze-thaw-
// freeze sequence: the second freeze stamps fresh provenance (the
// idempotency guard only protects an ACTIVE freeze).
func TestRefreezeAfterThawOverwritesProvenance(t *testing.T) {
	dir := t.TempDir()
	if err := FreezeStore(dir, "first"); err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	first, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}
	if err := ThawStore(dir); err != nil {
		t.Fatalf("ThawStore: %v", err)
	}
	if err := FreezeStore(dir, "second"); err != nil {
		t.Fatalf("re-freeze: %v", err)
	}

	m, err := ReadStoreManifest(dir)
	if err != nil {
		t.Fatalf("read re-frozen manifest: %v", err)
	}
	if !m.ReadOnly {
		t.Error("re-frozen store should be readonly")
	}
	if m.Owner != "second" {
		t.Errorf("owner = %q after re-freeze, want second (overwritten)", m.Owner)
	}
	// PublishedAt is second-granular, so an immediate re-freeze can
	// land in the same second; assert it did not move backwards.
	if m.PublishedAt.Before(first.PublishedAt) {
		t.Errorf("published_at moved backwards on re-freeze: %v -> %v", first.PublishedAt, m.PublishedAt)
	}
}

// TestErrStoreReadOnlySentinel guards the sentinel contract: callers
// (and later the api error taxonomy) detect read-only rejection via
// errors.Is across wrapping.
func TestErrStoreReadOnlySentinel(t *testing.T) {
	wrapped := fmt.Errorf("withwritebatch %q: %w", "label", ErrStoreReadOnly)
	if !errors.Is(wrapped, ErrStoreReadOnly) {
		t.Fatal("ErrStoreReadOnly must survive wrapping for errors.Is checks")
	}
	if ErrStoreReadOnly.Error() != "store is read-only" {
		t.Fatalf("sentinel message = %q, want %q", ErrStoreReadOnly.Error(), "store is read-only")
	}
}
