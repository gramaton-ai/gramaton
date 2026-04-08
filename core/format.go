package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gramaton-ai/gramaton/internal/version"
)

// ReadFormatVersion reads the store format version from the FORMAT
// file in the data directory. Returns 0 if the file does not exist
// (new or pre-format-version store).
func ReadFormatVersion(dataDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "FORMAT"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read FORMAT: %w", err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse FORMAT: %w", err)
	}
	return v, nil
}

// WriteFormatVersion writes the current store format version to the
// FORMAT file in the data directory.
func WriteFormatVersion(dataDir string) error {
	s := strconv.Itoa(version.StoreFormatVersion)
	return AtomicWriteFile(filepath.Join(dataDir, "FORMAT"), []byte(s), 0o600)
}

// CheckFormatVersion reads the store format version and returns an
// error if the store was created by a newer version of gramaton.
// If no FORMAT file exists (pre-format-version store), it writes
// the current version.
func CheckFormatVersion(dataDir string) error {
	v, err := ReadFormatVersion(dataDir)
	if err != nil {
		return err
	}

	if v == 0 {
		// No FORMAT file -- either new store or pre-format-version.
		// Write current version.
		return WriteFormatVersion(dataDir)
	}

	if v > version.StoreFormatVersion {
		return fmt.Errorf(
			"store format version %d is newer than this binary supports (%d); upgrade gramaton",
			v, version.StoreFormatVersion,
		)
	}

	// v <= StoreFormatVersion: compatible. Future migrations would
	// go here (e.g., if v == 1 && StoreFormatVersion == 2, migrate).

	return nil
}
