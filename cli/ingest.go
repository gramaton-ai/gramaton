package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var (
	ingestRecursive bool
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
	ingestCmd.Flags().StringVar(&ingestMessage, "commit-message", "", "commit message (default: auto-generated)")
	rootCmd.AddCommand(ingestCmd)
}

type ingestOutput struct {
	FilesProcessed int      `json:"files_processed"`
	RecordsCreated int      `json:"records_created"`
	Warnings       []string `json:"warnings,omitempty"`
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
	var files []string
	for _, arg := range args {
		expanded, err := expandPath(arg, ingestRecursive)
		if err != nil {
			return fmt.Errorf("expand path %q: %w", arg, err)
		}
		files = append(files, expanded...)
	}

	if len(files) == 0 {
		return writeError("no_files", "No files found to ingest", false)
	}

	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	var warnings []string
	var created int

	for _, path := range files {
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

		// Check content length limit.
		if len(content) > eng.cfg.Limits.MaxContentLength {
			warnings = append(warnings, fmt.Sprintf("skipped %s: exceeds max content length (%d bytes)", path, eng.cfg.Limits.MaxContentLength))
			continue
		}

		absPath, _ := filepath.Abs(path)

		props := graph.Properties{
			"content_full":      graph.StringProperty(content),
			"content_short":     graph.StringProperty(naiveSummary(content, 200)),
			"source_ref":        graph.StringProperty(absPath),
			"processing_status": graph.StringProperty("captured"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
			"access_count":      graph.Int64Property(0),
		}

		n := eng.graph.AddNode(props)
		for k, v := range n.Properties {
			eng.propIdx.Add(n.ID, k, v)
		}

		if err := eng.generateEmbeddings(context.Background(), n.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("embedding failed for %s: %s", path, err))
		}

		if numChunks, err := eng.chunkIfNeeded(context.Background(), n.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("chunking failed for %s: %s", path, err))
		} else if numChunks > 0 {
			warnings = append(warnings, fmt.Sprintf("%s chunked into %d segments", filepath.Base(path), numChunks))
		}

		created++
	}

	if created > 0 {
		msg := ingestMessage
		if msg == "" {
			msg = fmt.Sprintf("ingest %d files", created)
		}
		if _, err := eng.save(msg); err != nil {
			return writeError("save_error", err.Error(), false)
		}
	}

	return printJSON(ingestOutput{
		FilesProcessed: len(files),
		RecordsCreated: created,
		Warnings:       warnings,
	})
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
					continue // skip symlinks
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

func naiveSummary(content string, maxLen int) string {
	// Take first line or first maxLen characters, whichever is shorter.
	s := content
	if idx := strings.IndexByte(s, '\n'); idx >= 0 && idx < maxLen {
		s = s[:idx]
	}
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return strings.TrimSpace(s)
}
