package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// storeManifestFile is the name of the store manifest file in the
// data directory, a sibling of FORMAT, HEAD, and BRANCH. Because it
// lives in the data dir, the manifest travels with any copy, tar, or
// backup of the store.
const storeManifestFile = "STORE"

// ErrStoreReadOnly is returned by engine write paths (Save,
// WithWriteBatch) when the store is in store-level read-only mode --
// either frozen via the STORE manifest or forced via WithReadOnly.
// Check with errors.Is. The api layer is responsible for mapping it
// onto the standard error taxonomy.
var ErrStoreReadOnly = errors.New("store is read-only")

// StoreManifest is the store-level metadata persisted in the STORE
// file. The read-only flag marks the KNOWLEDGE as frozen: all logical
// writes are rejected and curation is disabled, while reads keep
// working. Derived local caches (indexes.db, vec.flat, jobs.db)
// remain writable -- they are rebuilt from the graph at startup by
// design and are excluded from backups.
type StoreManifest struct {
	// ReadOnly marks the store logically frozen. Set by FreezeStore,
	// cleared by ThawStore, honored by the engine at open time.
	ReadOnly bool `json:"readonly"`

	// Owner records who published (froze) the store. Optional; may
	// be empty. Preserved across ThawStore as provenance of the
	// original publication; overwritten by a subsequent FreezeStore.
	Owner string `json:"owner,omitempty"`

	// PublishedAt is when the store was frozen (UTC, RFC3339).
	// Preserved across ThawStore; overwritten by a subsequent
	// FreezeStore.
	PublishedAt time.Time `json:"published_at,omitzero"`
}

// ReadStoreManifest reads the STORE manifest from the data directory.
// An absent file returns the zero manifest (writable) with a nil
// error -- stores created before the manifest existed are writable by
// definition. An unparseable file returns an error: a corrupted
// manifest on a store that might be frozen must fail loud rather than
// silently open writable.
func ReadStoreManifest(dataDir string) (StoreManifest, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, storeManifestFile))
	if os.IsNotExist(err) {
		return StoreManifest{}, nil
	}
	if err != nil {
		return StoreManifest{}, fmt.Errorf("read STORE manifest: %w", err)
	}
	var m StoreManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return StoreManifest{}, fmt.Errorf("parse STORE manifest: %w", err)
	}
	return m, nil
}

// WriteStoreManifest writes the STORE manifest to the data directory
// atomically with full durability. The manifest is canonical store
// metadata (it decides whether a store accepts writes), not derived
// state, so it takes the same fsync treatment as FORMAT/HEAD/refs.
func WriteStoreManifest(dataDir string, m StoreManifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal STORE manifest: %w", err)
	}
	return AtomicWriteFile(filepath.Join(dataDir, storeManifestFile), data, 0o600)
}

// FreezeStore marks the store read-only, turning it into a frozen,
// shareable artifact. Sets readonly=true, owner (may be empty), and
// published_at=now (UTC, second precision). Offline primitive: it
// operates directly on the data dir with no engine needed, so the CLI
// can freeze a store that isn't being served.
//
// Freezing an already-frozen store is a no-op that preserves the
// original owner and published_at -- re-running freeze must not
// rewrite the provenance of the original publication. To re-stamp
// provenance, thaw first: a freeze after a thaw overwrites both
// fields.
func FreezeStore(dataDir, owner string) error {
	m, err := ReadStoreManifest(dataDir)
	if err != nil {
		return err
	}
	if m.ReadOnly {
		return nil
	}
	m.ReadOnly = true
	m.Owner = owner
	m.PublishedAt = time.Now().UTC().Truncate(time.Second)
	return WriteStoreManifest(dataDir, m)
}

// ThawStore clears the read-only flag, making the store writable
// again. Owner and published_at are PRESERVED: they record the
// provenance of the original publication and survive a thaw. A
// subsequent FreezeStore overwrites them. Offline primitive, like
// FreezeStore.
//
// Thawing a store that is not frozen is a no-op; in particular it
// does not create a STORE manifest on a store that never had one.
func ThawStore(dataDir string) error {
	m, err := ReadStoreManifest(dataDir)
	if err != nil {
		return err
	}
	if !m.ReadOnly {
		return nil
	}
	m.ReadOnly = false
	return WriteStoreManifest(dataDir, m)
}
