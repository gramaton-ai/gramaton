package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// newAttachSource fabricates a minimal shared-store artifact: a
// store dir with data/FORMAT at the current version plus a couple of
// content files (nested, to exercise the recursive copy). The
// engine-backed fixture lives in internal/setup's tests; here the
// on-disk shape is all the primitives look at.
func newAttachSource(t *testing.T, frozen bool) (storeDir, dataDir string) {
	t.Helper()
	storeDir = t.TempDir()
	dataDir = filepath.Join(storeDir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "refs", "heads"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"FORMAT":          strconv.Itoa(version.StoreFormatVersion),
		"HEAD":            "abc123",
		"refs/heads/main": "abc123",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dataDir, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if frozen {
		if err := core.FreezeStore(dataDir, "Ada Lovelace <ada@example.com>"); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir, dataDir
}

func TestResolveAttachSourceAcceptsBothShapes(t *testing.T) {
	storeDir, dataDir := newAttachSource(t, false)

	// Store dir containing data/.
	got, err := ResolveAttachSource(storeDir)
	if err != nil {
		t.Fatalf("store-dir shape: %v", err)
	}
	if got != dataDir {
		t.Errorf("store-dir shape resolved %q, want %q", got, dataDir)
	}

	// Data dir directly.
	got, err = ResolveAttachSource(dataDir)
	if err != nil {
		t.Fatalf("data-dir shape: %v", err)
	}
	if got != dataDir {
		t.Errorf("data-dir shape resolved %q, want %q", got, dataDir)
	}
}

func TestResolveAttachSourceRejections(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		if _, err := ResolveAttachSource(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("want error for a missing directory")
		}
	})

	t.Run("plain file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "archive.tar.gz")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveAttachSource(f)
		if err == nil || !strings.Contains(err.Error(), "not a store directory") {
			t.Fatalf("err = %v, want the not-a-directory rejection", err)
		}
	})

	t.Run("no FORMAT anywhere", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveAttachSource(dir)
		if err == nil || !strings.Contains(err.Error(), "doesn't look like a Gramaton store") {
			t.Fatalf("err = %v, want the missing-FORMAT rejection", err)
		}
		// The read-only validation must never stamp a FORMAT file
		// (core.CheckFormatVersion would).
		if _, serr := os.Stat(filepath.Join(dir, "data", "FORMAT")); !os.IsNotExist(serr) {
			t.Errorf("validation created a FORMAT file in the source, stat err = %v", serr)
		}
	})

	t.Run("format version mismatch", func(t *testing.T) {
		for _, tc := range []struct {
			v    string
			want string
		}{
			{"1", "older"},
			{"999", "newer"},
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "FORMAT"), []byte(tc.v), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ResolveAttachSource(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("FORMAT=%s: err = %v, want mention of %q", tc.v, err, tc.want)
			}
		}
	})
}

func TestAttachFrozenSourcePreservesProvenance(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, true)

	res, err := Attach(base, "shared", srcData)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if res.Name != "shared" || !res.SourceFrozen {
		t.Errorf("result = %+v, want Name=shared SourceFrozen=true", res)
	}

	// Copy landed, recursively.
	for _, rel := range []string{"FORMAT", "HEAD", filepath.Join("refs", "heads", "main")} {
		if _, err := os.Stat(filepath.Join(res.DataDir, rel)); err != nil {
			t.Errorf("copied data dir missing %s: %v", rel, err)
		}
	}

	// The copy's manifest is frozen with the source's provenance.
	m, err := core.ReadStoreManifest(res.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly || m.Owner != "Ada Lovelace <ada@example.com>" || m.PublishedAt.IsZero() {
		t.Errorf("copied manifest = %+v, want frozen with preserved provenance", m)
	}

	// Minimal per-store config: data_dir present, llm/author absent.
	raw, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatalf("per-store config: %v", err)
	}
	cfg := string(raw)
	if !strings.Contains(cfg, "data_dir:") {
		t.Errorf("config missing data_dir:\n%s", cfg)
	}
	for _, banned := range []string{"llm:", "author:", "api_key"} {
		if strings.Contains(cfg, banned) {
			t.Errorf("config must not contain %q:\n%s", banned, cfg)
		}
	}

	// The attached store shows up as a named store.
	if !Exists(base, "shared") {
		t.Error("attached store not visible to store.Exists")
	}
}

// TestAttachWritableSourceFreezesCopyOnly is the writable-artifact
// contract: the local copy is frozen (owner empty -- nothing is
// guessed), the source directory gains no STORE manifest and keeps
// its files untouched.
func TestAttachWritableSourceFreezesCopyOnly(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, false)

	res, err := Attach(base, "shared", srcData)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if res.SourceFrozen {
		t.Error("SourceFrozen = true for a writable artifact")
	}

	m, err := core.ReadStoreManifest(res.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly {
		t.Error("local copy must be frozen even when the source is writable")
	}
	if m.Owner != "" {
		t.Errorf("locally-stamped owner = %q, want empty", m.Owner)
	}

	// Source untouched: no manifest appeared there.
	if _, err := os.Stat(filepath.Join(srcData, "STORE")); !os.IsNotExist(err) {
		t.Errorf("Attach froze the SOURCE, stat err = %v", err)
	}
}

// TestAttachThawedSourcePreservesProvenance pins the thawed-source
// contract: thaw keeps owner/published_at as provenance of the
// original publication, and the attached copy must carry them
// VERBATIM -- re-stamping a fresh published_at would assert the
// original publisher published the store at attach time, over content
// they may have modified after thawing.
func TestAttachThawedSourcePreservesProvenance(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, true)
	if err := core.ThawStore(srcData); err != nil {
		t.Fatalf("thaw source: %v", err)
	}
	orig, err := core.ReadStoreManifest(srcData)
	if err != nil {
		t.Fatal(err)
	}
	if orig.ReadOnly || orig.Owner == "" || orig.PublishedAt.IsZero() {
		t.Fatalf("fixture manifest = %+v, want thawed with provenance", orig)
	}

	res, err := Attach(base, "shared", srcData)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if res.SourceFrozen {
		t.Error("SourceFrozen = true for a thawed source")
	}

	m, err := core.ReadStoreManifest(res.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly {
		t.Error("local copy must be frozen")
	}
	if m.Owner != orig.Owner {
		t.Errorf("copied owner = %q, want the source's %q verbatim", m.Owner, orig.Owner)
	}
	if !m.PublishedAt.Equal(orig.PublishedAt) {
		t.Errorf("copied published_at = %v, want the source's %v verbatim (no fresh stamp)",
			m.PublishedAt, orig.PublishedAt)
	}
}

func TestAttachNameCollision(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, true)

	if _, err := Attach(base, "shared", srcData); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	_, err := Attach(base, "shared", srcData)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Attach err = %v, want the already-exists rejection", err)
	}
}

// TestAttachRefusesPreexistingStoreHome pins the HOME-level collision
// check: a store home holding only a config.yaml (no data/) passes
// store.Create's data-dir probe, but Attach must refuse rather than
// copy into it -- its failure cleanup removes the home wholesale, so
// proceeding could delete files this attach never created.
func TestAttachRefusesPreexistingStoreHome(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, true)

	home := filepath.Join(base, "stores", "shared")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	preexisting := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(preexisting, []byte("# hand-placed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Attach(base, "shared", srcData)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Attach err = %v, want the already-exists rejection", err)
	}

	// Nothing was deleted or overwritten.
	raw, rerr := os.ReadFile(preexisting)
	if rerr != nil {
		t.Fatalf("pre-existing config gone after refused attach: %v", rerr)
	}
	if string(raw) != "# hand-placed\n" {
		t.Errorf("pre-existing config rewritten:\n%s", raw)
	}
}

// TestAttachRefusesLiveSourceServer pins the live-source guard: a
// server.json beside the source data dir naming a live PID (this test
// process's own, the freeze/thaw guard pattern) refuses the attach
// before anything is copied; a stale one naming a dead PID does not.
func TestAttachRefusesLiveSourceServer(t *testing.T) {
	base := t.TempDir()
	srcStoreDir, srcData := newAttachSource(t, true)

	serverJSON := filepath.Join(srcStoreDir, "server.json")
	writeInfo := func(pid int) {
		t.Helper()
		body := fmt.Sprintf(`{"pid": %d, "port": 1, "started_at": "2026-01-01T00:00:00Z"}`, pid)
		if err := os.WriteFile(serverJSON, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeInfo(os.Getpid())
	_, err := Attach(base, "shared", srcData)
	if err == nil || !strings.Contains(err.Error(), "Stop the server serving this store first") {
		t.Fatalf("Attach err = %v, want the stop-the-server refusal", err)
	}
	if _, serr := os.Stat(filepath.Join(base, "stores", "shared")); !os.IsNotExist(serr) {
		t.Errorf("refused attach left a store home behind, stat err = %v", serr)
	}

	// A stale server.json (dead PID) must not block the attach.
	writeInfo(1 << 30)
	if _, err := Attach(base, "shared", srcData); err != nil {
		t.Fatalf("Attach with stale server.json: %v", err)
	}
}

// TestAttachFailureLeavesNoTrace pins the cleanup contract: a copy
// failure (irregular entry in the source) removes the half-built
// store home entirely.
func TestAttachFailureLeavesNoTrace(t *testing.T) {
	base := t.TempDir()
	_, srcData := newAttachSource(t, true)
	// A fifo/symlink is "irregular"; symlink is portable enough for
	// the platforms this test runs on and fails the copy.
	if err := os.Symlink(filepath.Join(srcData, "HEAD"), filepath.Join(srcData, "HEAD-link")); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	_, err := Attach(base, "shared", srcData)
	if err == nil {
		t.Fatal("Attach should fail on an irregular source entry")
	}
	if _, serr := os.Stat(filepath.Join(base, "stores", "shared")); !os.IsNotExist(serr) {
		t.Errorf("failed Attach left the store home behind, stat err = %v", serr)
	}
}

func TestDefaultAttachName(t *testing.T) {
	base := t.TempDir()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{"data dir uses parent name", filepath.Join("/tmp", "team-notes", "data"), "team-notes"},
		{"plain dir uses own name", filepath.Join("/tmp", "research_2026"), "research_2026"},
		{"spaces sanitized", filepath.Join("/tmp", "Ada's Store", "data"), "Ada-s-Store"},
		{"nothing usable falls back", filepath.Join("/tmp", "---"), "shared-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultAttachName(tt.src, base); got != tt.want {
				t.Errorf("DefaultAttachName(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}

	t.Run("collision falls back", func(t *testing.T) {
		if err := Create(base, "taken"); err != nil {
			t.Fatal(err)
		}
		if got := DefaultAttachName(filepath.Join("/tmp", "taken", "data"), base); got != "shared-store" {
			t.Errorf("colliding default = %q, want shared-store", got)
		}
	})
}
