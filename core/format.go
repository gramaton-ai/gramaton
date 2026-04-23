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

// CheckFormatVersion reads the store format version and enforces the
// boot-time compatibility gate. Behaviors by read value:
//
//   - v == 0 (no FORMAT file): treated as a fresh store. Writes the
//     current version and returns nil.
//   - v == current: compatible. Returns nil.
//   - v < current: OLDER than current. Returns an error telling the
//     user to run `gramaton migrate`. No auto-upgrade at boot by
//     design (see feedback_ad_hoc_migrations). Migration paths call
//     this function's peer, ReadFormatVersion, to inspect without
//     enforcing.
//   - v > current: NEWER than the running binary. Returns an error
//     asking the user to upgrade gramaton.
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

	if v < version.StoreFormatVersion {
		return fmt.Errorf(
			"store format version %d is older than this binary supports (%d); run `gramaton migrate` to upgrade",
			v, version.StoreFormatVersion,
		)
	}

	return nil
}
