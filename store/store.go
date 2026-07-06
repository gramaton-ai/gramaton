// Package store provides named store resolution, validation, and
// lifecycle operations. Each named store is an isolated knowledge
// graph with its own data directory, server process, and optional
// config override.
//
// Layout:
//
//	~/.gramaton/               default (unnamed) store
//	  config.yaml              global config
//	  data/
//	  server.json
//	  stores/
//	    work/                  named store "work"
//	      config.yaml          optional, inherits global if absent
//	      data/
//	      server.json
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidateName checks that a store name is valid. Names must be 1-64
// characters of alphanumeric, hyphen, or underscore. "default" is
// reserved as an alias for the unnamed store in rename operations.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("store name is required (use 1-64 alphanumeric, hyphen, or underscore characters)")
	}
	if name == "default" {
		return fmt.Errorf("%q is reserved for the unnamed store", name)
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("store name must be 1-64 alphanumeric, hyphen, or underscore characters")
	}
	return nil
}

// Resolve returns the config directory for a named store. If name is
// empty, returns baseDir unchanged (the unnamed default store).
func Resolve(baseDir, name string) string {
	if name == "" {
		return baseDir
	}
	return filepath.Join(baseDir, "stores", name)
}

// StoreInfo describes a store for listing.
type StoreInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default,omitempty"`
	// Remote is true for a remote-client store (config.yaml with
	// remote.url and no local data/); RemoteURL carries the server it
	// points at. A remote store's Path is empty -- its data lives on
	// another machine.
	Remote    bool   `json:"remote,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

// List returns all stores (default + named), sorted alphabetically
// with default first. A store is included when it has a local data/ dir
// (local) OR a config.yaml with remote.url (a remote client); a dir with
// neither is a half-built directory and is skipped. Both the default
// and named stores can be remote.
func List(baseDir string) []StoreInfo {
	var stores []StoreInfo

	// Default store: local (owns data/) or a remote client (config with
	// remote.url and no local data/).
	if DefaultExists(baseDir) {
		stores = append(stores, StoreInfo{
			Name:    "(default)",
			Path:    filepath.Join(baseDir, "data"),
			Default: true,
		})
	} else if url := storeRemoteURL(baseDir); url != "" {
		stores = append(stores, StoreInfo{
			Name:      "(default)",
			Default:   true,
			Remote:    true,
			RemoteURL: url,
		})
	}

	// Named stores. Single pass: probe the config only for a dir that
	// lacks data/ (a remote candidate), so a local store never re-reads
	// its config here.
	storesDir := filepath.Join(baseDir, "stores")
	entries, err := os.ReadDir(storesDir)
	if err != nil {
		return stores
	}

	type namedStore struct {
		name string
		info StoreInfo
	}
	var found []namedStore
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(storesDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
			found = append(found, namedStore{e.Name(), StoreInfo{Name: e.Name(), Path: filepath.Join(dir, "data")}})
		} else if url := storeRemoteURL(dir); url != "" {
			found = append(found, namedStore{e.Name(), StoreInfo{Name: e.Name(), Remote: true, RemoteURL: url}})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	for _, f := range found {
		stores = append(stores, f.info)
	}

	return stores
}

// Exists checks if a named store exists: a local store has a data/
// directory; a remote-client store has a config.yaml with remote.url
// and no data/. Either counts, so a remote store is not silently
// clobbered by a create/attach that only probes for data/.
func Exists(baseDir, name string) bool {
	dir := Resolve(baseDir, name)
	if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
		return true
	}
	return storeRemoteURL(dir) != ""
}

// DefaultExists checks if the unnamed default store has data.
func DefaultExists(baseDir string) bool {
	dataDir := filepath.Join(baseDir, "data")
	_, err := os.Stat(dataDir)
	return err == nil
}

// Create creates a new named store directory structure.
func Create(baseDir, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if Exists(baseDir, name) {
		return fmt.Errorf("store %q already exists", name)
	}
	dir := Resolve(baseDir, name)
	dataDir := filepath.Join(dir, "data")
	return os.MkdirAll(dataDir, 0o700)
}

// Delete removes a named store and all its data. Caller should
// verify no server is running for this store before calling.
func Delete(baseDir, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !Exists(baseDir, name) {
		return fmt.Errorf("store %q does not exist", name)
	}
	return os.RemoveAll(Resolve(baseDir, name))
}

// Rename renames a store. Use "default" to refer to the unnamed
// store. Caller should verify no server is running for the source
// store before calling.
func Rename(baseDir, oldName, newName string) error {
	if oldName == newName {
		return fmt.Errorf("old and new names are the same")
	}

	// Validate the non-"default" names.
	if oldName != "default" {
		if err := ValidateName(oldName); err != nil {
			return fmt.Errorf("old name: %w", err)
		}
	}
	if newName != "default" {
		if err := ValidateName(newName); err != nil {
			return fmt.Errorf("new name: %w", err)
		}
	}

	// Check source exists.
	if oldName == "default" {
		if !DefaultExists(baseDir) {
			return fmt.Errorf("default store has no data")
		}
	} else {
		if !Exists(baseDir, oldName) {
			return fmt.Errorf("store %q does not exist", oldName)
		}
	}

	// Check target doesn't exist.
	if newName == "default" {
		if DefaultExists(baseDir) {
			return fmt.Errorf("default store already has data; delete it first")
		}
	} else {
		if Exists(baseDir, newName) {
			return fmt.Errorf("store %q already exists", newName)
		}
	}

	switch {
	case oldName == "default":
		// default -> named: move data/ from base to stores/<new>/, then
		// pin the new named store's own data_dir. The default store read
		// the GLOBAL config.yaml (which stays at base) and had no
		// per-store config of its own, so without a fresh pin the
		// config-less named store would let the global data_dir bleed
		// through -- the exact footgun WriteDataDirConfig guards against.
		newDir := Resolve(baseDir, newName)
		if err := os.MkdirAll(newDir, 0o700); err != nil {
			return err
		}
		oldData := filepath.Join(baseDir, "data")
		newData := filepath.Join(newDir, "data")
		if err := os.Rename(oldData, newData); err != nil {
			return err
		}
		_, err := WriteDataDirConfig(newDir, newName)
		return err

	case newName == "default":
		// named -> default: move data/ from stores/<old>/ to base, then
		// drop the old store home. Its per-store config.yaml (pinning the
		// old absolute path) goes with it; the default store resolves
		// <base>/data through the global config, so no new pin is needed.
		oldDir := Resolve(baseDir, oldName)
		oldData := filepath.Join(oldDir, "data")
		newData := filepath.Join(baseDir, "data")
		// A remote-client store has no local data/ to move (Exists now
		// admits it, so the precondition above passes). Refuse with a
		// clear message rather than letting os.Rename fail on a missing
		// dir; making a remote store the default would also clobber the
		// global config.
		if _, err := os.Stat(oldData); err != nil {
			return fmt.Errorf("store %q has no local data to move to the default store "+
				"(a remote-client store cannot become the default this way; delete it and re-run `gramaton remote add` without --store)", oldName)
		}
		if err := os.Rename(oldData, newData); err != nil {
			return err
		}
		// Clean up the empty store directory.
		return os.RemoveAll(oldDir)

	default:
		// named -> named: rename the entire store directory (config.yaml
		// travels verbatim). Re-pin data_dir only for a LOCAL store --
		// the moved config still names the OLD absolute data path. A
		// remote-client store has no local data/ and must not gain a
		// spurious data_dir (its config carries remote.url, not a data
		// pin), so skip the repin when there is no data/ dir.
		storesDir := filepath.Join(baseDir, "stores")
		oldDir := filepath.Join(storesDir, oldName)
		newDir := filepath.Join(storesDir, newName)
		if err := os.Rename(oldDir, newDir); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(newDir, "data")); err != nil {
			return nil // remote-client store: no data_dir to re-pin
		}
		_, err := repinDataDir(newDir, newName)
		return err
	}
}
