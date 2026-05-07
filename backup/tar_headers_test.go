package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupTarHeaderNamesUseForwardSlashes guards a Windows-only
// regression: filepath.Join in the archive walker produced
// data\foo\bar header names on Windows, in violation of the POSIX
// tar spec (which mandates forward slashes). Restore on Windows
// then re-extracted entries to bogus paths or skipped them
// entirely, leaving callers with empty bbolt files and nil-bucket
// panics. Verifies (1) every entry uses forward slashes and
// (2) Restore lays the file out at the correct OS-native path.
func TestBackupTarHeaderNamesUseForwardSlashes(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := t.TempDir()

	// Nested chunk-style path; multiple separators force the bug
	// to surface on Windows.
	nestedDir := filepath.Join(dataDir, "objects", "ab", "cd")
	if err := os.MkdirAll(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nestedDir, "ef1234"), "chunk-content")

	writeFile(t, filepath.Join(dataDir, "HEAD"), "abc")
	if err := os.MkdirAll(filepath.Join(dataDir, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dataDir, "refs", "main"), "abc")

	snap, err := ReadSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	archivePath, err := CreateSnapshot(snap, dataDir, "", backupDir)
	if err != nil {
		t.Fatal(err)
	}

	// Every entry name must use forward slashes per the tar spec,
	// even on Windows. A backslash here means the archive is
	// malformed.
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	sawNested := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, `\`) {
			t.Errorf("tar entry name %q contains a backslash; tar headers must use forward slashes", h.Name)
		}
		if h.Name == "data/objects/ab/cd/ef1234" {
			sawNested = true
		}
	}
	if !sawNested {
		t.Error("nested chunk entry data/objects/ab/cd/ef1234 not found in archive")
	}

	// Restore and verify the nested file lands at the OS-native
	// path. If header names were backslashed, Restore on Windows
	// would create a single literal-named file or skip it; either
	// way the OS-native filepath.Join check below fails.
	restoreDir := t.TempDir()
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored := filepath.Join(restoreDir, "objects", "ab", "cd", "ef1234")
	body, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("restored file %s missing or unreadable: %v", restored, err)
	}
	if string(body) != "chunk-content" {
		t.Errorf("restored content = %q; want chunk-content", body)
	}
}
