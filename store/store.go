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
}

// List returns all stores (default + named), sorted alphabetically
// with default first.
func List(baseDir string) []StoreInfo {
	var stores []StoreInfo

	// Default store.
	if DefaultExists(baseDir) {
		stores = append(stores, StoreInfo{
			Name:    "(default)",
			Path:    filepath.Join(baseDir, "data"),
			Default: true,
		})
	}

	// Named stores.
	storesDir := filepath.Join(baseDir, "stores")
	entries, err := os.ReadDir(storesDir)
	if err != nil {
		return stores
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dataDir := filepath.Join(storesDir, e.Name(), "data")
		if _, err := os.Stat(dataDir); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		stores = append(stores, StoreInfo{
			Name: name,
			Path: filepath.Join(storesDir, name, "data"),
		})
	}

	return stores
}

// Exists checks if a named store exists (has a data/ directory).
func Exists(baseDir, name string) bool {
	dir := Resolve(baseDir, name)
	dataDir := filepath.Join(dir, "data")
	_, err := os.Stat(dataDir)
	return err == nil
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
		// default -> named: move data/ from base to stores/<new>/
		newDir := Resolve(baseDir, newName)
		if err := os.MkdirAll(newDir, 0o700); err != nil {
			return err
		}
		oldData := filepath.Join(baseDir, "data")
		newData := filepath.Join(newDir, "data")
		return os.Rename(oldData, newData)

	case newName == "default":
		// named -> default: move data/ from stores/<old>/ to base
		oldDir := Resolve(baseDir, oldName)
		oldData := filepath.Join(oldDir, "data")
		newData := filepath.Join(baseDir, "data")
		if err := os.Rename(oldData, newData); err != nil {
			return err
		}
		// Clean up the empty store directory.
		return os.RemoveAll(oldDir)

	default:
		// named -> named: rename the entire store directory.
		storesDir := filepath.Join(baseDir, "stores")
		oldDir := filepath.Join(storesDir, oldName)
		newDir := filepath.Join(storesDir, newName)
		return os.Rename(oldDir, newDir)
	}
}
