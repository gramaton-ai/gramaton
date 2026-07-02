package store

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/internal/version"
)

// Attaching a shared read-only store.
//
// A store shared as a directory (copied, untarred from a backup
// export, synced) is "attached" by registering it as a local named
// store: the data dir is COPIED under stores/<name>/ (the received
// artifact stays pristine), the local copy's STORE manifest is
// frozen unconditionally, and a minimal per-store config.yaml is
// written. The frozen manifest is the single switch every gramaton
// surface keys read-only enforcement off -- engine backstops, api
// guards, response envelope, MCP write-tool filtering -- so
// freezing the copy makes the whole chain hold with no extra
// plumbing, even when the sender never ran `gramaton store freeze`.
//
// Two callers share these primitives: the setup wizard's read-only
// route (internal/setup/step_readonly.go, for machines whose ONLY
// gramaton is the shared store) and `gramaton store attach`
// (cli/store.go, for adding a shared store alongside an existing
// writable install).

// AttachResult reports a completed Attach.
type AttachResult struct {
	// Name is the local store name the store was registered under.
	Name string

	// StoreDir is the named store's home (stores/<name>/).
	StoreDir string

	// DataDir is the copied data directory (StoreDir/data).
	DataDir string

	// ConfigPath is the minimal per-store config.yaml written.
	ConfigPath string

	// Manifest is the STORE manifest on the local copy AFTER the
	// unconditional freeze; ReadOnly is always true. Owner and
	// PublishedAt carry the source's publication provenance when the
	// source was frozen.
	Manifest core.StoreManifest

	// SourceFrozen reports whether the source artifact itself was
	// frozen. When false, the local copy was frozen here (owner
	// empty) and the source was left writable and untouched.
	SourceFrozen bool
}

// ResolveAttachSource validates a received store directory and
// returns its data dir. Accepts either a data dir directly (it
// contains FORMAT) or a store dir containing data/. Enforces the
// format-version gate WITHOUT writing: core.CheckFormatVersion
// stamps a missing FORMAT file, which must never happen to a
// received artifact, so the comparison runs on ReadFormatVersion
// instead.
func ResolveAttachSource(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no directory at %s", path)
		}
		return "", fmt.Errorf("can't access %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a store directory (if you received an archive, unpack it first)", path)
	}

	// A data dir is identified by its FORMAT file; a store dir
	// carries the data dir at data/. Checked in that order so a
	// store dir that ALSO has a stray FORMAT file at top level is
	// taken at face value (FORMAT wins -- it is the data-dir marker).
	dataDir := path
	if _, err := os.Stat(filepath.Join(path, "FORMAT")); err != nil {
		nested := filepath.Join(path, "data")
		if _, nerr := os.Stat(filepath.Join(nested, "FORMAT")); nerr != nil {
			return "", fmt.Errorf("%s doesn't look like a Gramaton store: no FORMAT file there or under data/", path)
		}
		dataDir = nested
	}

	v, err := core.ReadFormatVersion(dataDir)
	if err != nil {
		return "", fmt.Errorf("can't read the store's FORMAT file: %w", err)
	}
	if v < version.StoreFormatVersion {
		return "", fmt.Errorf(
			"store format version %d is older than this gramaton supports (%d); ask whoever shared it to upgrade gramaton, run `gramaton migrate`, and re-share",
			v, version.StoreFormatVersion)
	}
	if v > version.StoreFormatVersion {
		return "", fmt.Errorf(
			"store format version %d is newer than this gramaton supports (%d); upgrade gramaton",
			v, version.StoreFormatVersion)
	}

	return dataDir, nil
}

// Attach registers the store at srcDataDir (a validated data dir --
// see ResolveAttachSource) as the named store `name` under baseDir:
// creates the store home, copies the data dir into it, freezes the
// LOCAL COPY's manifest, and writes the minimal per-store config.
// The source directory is never modified. On any failure after
// creation the half-built store home is removed, so a failed attach
// leaves no trace.
//
// The per-store config carries data_dir and nothing else: without
// it, the engine's global-then-store config merge would let a
// global data_dir bleed through and open the WRONG store (see
// cli/store.go storeEffectiveConfig); everything else -- and
// deliberately no llm or author block -- inherits from the global
// config and defaults, because a read-only consumer never writes,
// runs no curation, and stamps no authorship.
func Attach(baseDir, name, srcDataDir string) (AttachResult, error) {
	src, err := core.ReadStoreManifest(srcDataDir)
	if err != nil {
		// Fail loud, same rationale as the engine: a corrupted
		// manifest on a store that might be frozen must not be
		// silently treated as anything.
		return AttachResult{}, fmt.Errorf("read source STORE manifest: %w", err)
	}

	if err := Create(baseDir, name); err != nil {
		return AttachResult{}, err
	}
	storeDir := Resolve(baseDir, name)
	fail := func(err error) (AttachResult, error) {
		_ = os.RemoveAll(storeDir)
		return AttachResult{}, err
	}

	dataDir := filepath.Join(storeDir, "data")
	if err := copyDirContents(srcDataDir, dataDir); err != nil {
		return fail(fmt.Errorf("copy store data: %w", err))
	}

	// Freeze the copy unconditionally. For an already-frozen source
	// this is a no-op that preserves the original owner/published_at
	// provenance; for a writable artifact it stamps the copy frozen.
	// Owner deliberately empty in that case: guessing an identity to
	// stamp onto someone else's artifact would be wrong.
	if err := core.FreezeStore(dataDir, src.Owner); err != nil {
		return fail(fmt.Errorf("freeze local copy: %w", err))
	}

	cfgPath := filepath.Join(storeDir, "config.yaml")
	body := fmt.Sprintf(
		"# Gramaton per-store config for attached read-only store %q.\n"+
			"# Deliberately minimal: everything else inherits from the global\n"+
			"# config (if any) and built-in defaults. No llm or author blocks --\n"+
			"# a read-only consumer never writes, runs no curation, and stamps\n"+
			"# no authorship.\n"+
			"data_dir: %q\n",
		name, dataDir)
	if err := core.AtomicWriteFile(cfgPath, []byte(body), 0o600); err != nil {
		return fail(fmt.Errorf("write per-store config: %w", err))
	}

	m, err := core.ReadStoreManifest(dataDir)
	if err != nil {
		return fail(fmt.Errorf("read back local manifest: %w", err))
	}

	return AttachResult{
		Name:         name,
		StoreDir:     storeDir,
		DataDir:      dataDir,
		ConfigPath:   cfgPath,
		Manifest:     m,
		SourceFrozen: src.ReadOnly,
	}, nil
}

// DefaultAttachName derives a store-name suggestion from the source
// directory: the data dir's parent name when the path ends in the
// conventional data/, the directory's own name otherwise. Sanitized
// to ValidateName's alphabet; falls back to "shared-store" when
// nothing usable survives or the derived name is already taken.
func DefaultAttachName(srcDataDir, baseDir string) string {
	dir := srcDataDir
	if filepath.Base(dir) == "data" {
		dir = filepath.Dir(dir)
	}
	name := sanitizeStoreName(filepath.Base(dir))
	if ValidateName(name) != nil || Exists(baseDir, name) {
		return "shared-store"
	}
	return name
}

// sanitizeStoreName maps an arbitrary directory name onto
// ValidateName's alphabet: disallowed runes become hyphens, leading
// non-alphanumerics are trimmed (names must start alphanumeric), and
// the result is capped at the 64-char limit.
func sanitizeStoreName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "-_")
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// copyDirContents recursively copies the directories and regular
// files under src into dst (created if needed), 0700 dirs / 0600
// files per the ~/.gramaton permission convention. Irregular entries
// (symlinks, sockets) fail loud: a shared store artifact should
// contain only plain files, and silently skipping one would produce
// a subtly incomplete copy.
func copyDirContents(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported entry in store copy: %s (not a regular file)", path)
		}
		return copyFileContents(path, target)
	})
}

// copyFileContents copies one regular file. Plain create+copy (no
// tmp+rename): the destination tree is brand new and removed
// wholesale on any failure, so partial files can't leak into a live
// store.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return out.Close()
}
