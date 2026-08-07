package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	ingestRecursive    bool
	ingestAllowSimilar bool
	ingestMessage   string
)

var ingestCmd = &cobra.Command{
	Use:   "ingest <file> [file2 ...]",
	Short: "Ingest text files into the knowledge store",
	Long: `Reads text files from disk and creates knowledge records for each.
Sets source_ref to the file path, generates a naive summary from
the first 200 characters, and marks records as captured (pending
LLM classification).

All files in a single invocation are committed as one batch.
Generates embeddings if a provider is configured.

Supported: .md, .txt, .json, .yaml, .yml, .csv, .html, .xml, .go,
.py, .rs, .ts, .js, and other plain text formats.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runIngest,
}

func init() {
	ingestCmd.Flags().BoolVar(&ingestRecursive, "recursive", false, "recursively ingest directories")
	ingestCmd.Flags().BoolVar(&ingestAllowSimilar, "allow-similar", false, "disable similar-record holds for this ingest (bulk-import escape)")
	ingestCmd.Flags().StringVar(&ingestMessage, "commit-message", "", "commit message (default: auto-generated)")
	rootCmd.AddCommand(ingestCmd)
}

// Binary file extensions to reject.
var binaryExts = map[string]bool{
	".docx": true, ".doc": true, ".pdf": true, ".xlsx": true, ".xls": true,
	".pptx": true, ".ppt": true, ".zip": true, ".tar": true, ".gz": true,
	".bz2": true, ".7z": true, ".rar": true, ".png": true, ".jpg": true,
	".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".svg": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".o": true,
	".a": true, ".class": true, ".jar": true, ".wasm": true,
	".onnx": true, ".gguf": true, ".bin": true, ".safetensors": true,
}

func runIngest(cmd *cobra.Command, args []string) error {
	// Expand file list.
	var paths []string
	for _, arg := range args {
		expanded, err := expandPath(arg, ingestRecursive)
		if err != nil {
			return fmt.Errorf("expand path %q: %w", arg, err)
		}
		paths = append(paths, expanded...)
	}

	if len(paths) == 0 {
		return writeError("no_files", "No files found to ingest", false)
	}

	// Read files and build the upload payload.
	type ingestFile struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	var files []ingestFile
	var warnings []string

	for _, path := range paths {
		ext := strings.ToLower(filepath.Ext(path))
		if binaryExts[ext] {
			warnings = append(warnings, fmt.Sprintf("skipped binary file: %s", path))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to read %s: %s", path, err))
			continue
		}

		content := string(data)
		if len(content) == 0 {
			warnings = append(warnings, fmt.Sprintf("skipped empty file: %s", path))
			continue
		}

		absPath, _ := filepath.Abs(path)
		files = append(files, ingestFile{
			Filename: absPath,
			Content:  content,
		})
	}

	if len(files) == 0 {
		return writeError("no_files", "No valid files to ingest", false)
	}

	// Send to server via ingest API.
	resp, err := serverPost("/v1/ingest", map[string]any{
		"files":         files,
		"allow_similar": ingestAllowSimilar,
	})
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	return printEnvelope(resp)
}

// isSymlink returns true if the path is a symbolic link.
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func expandPath(path string, recursive bool) ([]string, error) {
	// Reject symlinks at the top level.
	if isSymlink(path) {
		return nil, fmt.Errorf("symlinks not followed: %s", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		// Try as glob.
		matches, globErr := filepath.Glob(path)
		if globErr != nil || len(matches) == 0 {
			return nil, fmt.Errorf("path not found: %s", path)
		}
		return matches, nil
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	if !recursive {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() {
				fp := filepath.Join(path, e.Name())
				if isSymlink(fp) {
					continue
				}
				files = append(files, fp)
			}
		}
		return files, nil
	}

	// Recursive walk -- skip symlinks.
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if isSymlink(p) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}
