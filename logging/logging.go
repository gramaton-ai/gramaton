// Package logging provides structured JSON logging with file rotation
// and compression. Built on log/slog.
package logging

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gramaton-ai/gramaton/config"
)

// New creates a slog.Logger that writes JSON to a rotating log file.
// If foreground is true, also writes to stderr. The log file is
// created at <dir>/gramaton.log.
func New(cfg config.LoggingConfig, dir string, foreground bool) (*slog.Logger, *RotatingWriter, error) {
	logPath := filepath.Join(dir, "gramaton.log")

	rw, err := NewRotatingWriter(logPath, int64(cfg.RotateSizeMB)*1024*1024, int64(cfg.MaxSizeMB)*1024*1024)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}

	var w io.Writer = rw
	if foreground {
		w = io.MultiWriter(rw, os.Stderr)
	}

	level := parseLevel(cfg.Level)
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	})

	return slog.New(handler), rw, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// RotatingWriter is an io.WriteCloser that rotates and compresses
// log files when they exceed a size threshold, and enforces a total
// disk budget by deleting the oldest compressed files.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string // current log file path
	file     *os.File
	size     int64 // current file size
	rotateAt int64 // rotate when file exceeds this
	maxTotal int64 // total disk budget for all log files
}

// NewRotatingWriter creates a writer that rotates at rotateAt bytes
// and enforces maxTotal bytes across all log files.
func NewRotatingWriter(path string, rotateAt, maxTotal int64) (*RotatingWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	return &RotatingWriter{
		path:     path,
		file:     f,
		size:     info.Size(),
		rotateAt: rotateAt,
		maxTotal: maxTotal,
	}, nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Check if rotation is needed before writing.
	if w.size+int64(len(p)) > w.rotateAt {
		if err := w.rotate(); err != nil {
			// Rotation may have closed w.file mid-sequence (e.g.,
			// compress failed after close, open-new failed, etc.)
			// so writing to w.file would hit a bad fd. Reopen the
			// log path defensively; if that also fails, surface the
			// error rather than silently dropping every future
			// write. (Previously we ignored the rotate error and
			// wrote to a stale fd, losing entries indefinitely.)
			_, _ = fmt.Fprintf(os.Stderr, "log rotation failed: %s\n", err)
			if reopenErr := w.reopenCurrent(); reopenErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "log reopen after rotation failure failed: %s\n", reopenErr)
				return 0, reopenErr
			}
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// reopenCurrent reopens w.path and swaps w.file + w.size. Used after
// a rotate() failure to guarantee w.file is a valid writable fd.
// Best-effort closes the existing handle first in case it is still
// open (double-close is a noop).
func (w *RotatingWriter) reopenCurrent() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	w.file = f
	if info, statErr := f.Stat(); statErr == nil {
		w.size = info.Size()
	} else {
		w.size = 0
	}
	return nil
}

// Close closes the current log file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// rotate closes the current file, compresses it, opens a new one,
// and enforces the disk budget.
func (w *RotatingWriter) rotate() error {
	// Close current file.
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close current log: %w", err)
	}

	// Find the next available index.
	idx := w.nextIndex()
	gzPath := fmt.Sprintf("%s.%d.gz", w.path, idx)

	// Compress the current file.
	if err := compressFile(w.path, gzPath); err != nil {
		return fmt.Errorf("compress log: %w", err)
	}

	// Remove the uncompressed original.
	os.Remove(w.path)

	// Enforce disk budget.
	w.enforceBudget()

	// Open a new file.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open new log: %w", err)
	}

	w.file = f
	w.size = 0
	return nil
}

// nextIndex finds the next rotation index by scanning existing .gz files.
func (w *RotatingWriter) nextIndex() int {
	files := w.compressedFiles()
	if len(files) == 0 {
		return 1
	}
	// Parse highest index.
	max := 0
	base := filepath.Base(w.path)
	for _, f := range files {
		name := filepath.Base(f)
		var idx int
		if _, err := fmt.Sscanf(name, base+".%d.gz", &idx); err == nil {
			if idx > max {
				max = idx
			}
		}
	}
	return max + 1
}

// compressedFiles returns all .gz log files sorted by modification time
// (oldest first).
func (w *RotatingWriter) compressedFiles() []string {
	pattern := w.path + ".*.gz"
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return nil
	}

	type fileInfo struct {
		path    string
		modTime int64
	}
	var infos []fileInfo
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		infos = append(infos, fileInfo{path: m, modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].modTime < infos[j].modTime
	})

	result := make([]string, len(infos))
	for i, info := range infos {
		result[i] = info.path
	}
	return result
}

// enforceBudget deletes the oldest compressed files until total size
// is under the budget. Stat/Remove failures are surfaced via the
// shared stderr writer (same channel used elsewhere in this file)
// so disk-budget overshoot is observable. Previously these errors
// were silently dropped, which let the budget overshoot
// indefinitely with no operator signal.
func (w *RotatingWriter) enforceBudget() {
	files := w.compressedFiles()
	if len(files) == 0 {
		return
	}

	var totalSize int64
	sizes := make(map[string]int64, len(files))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logging: budget stat failed: %s: %v\n", f, err)
			continue
		}
		sizes[f] = info.Size()
		totalSize += info.Size()
	}

	// Delete oldest files until under budget.
	for _, f := range files {
		if totalSize <= w.maxTotal {
			break
		}
		if err := os.Remove(f); err != nil {
			fmt.Fprintf(os.Stderr, "logging: budget remove failed: %s: %v\n", f, err)
			continue
		}
		totalSize -= sizes[f]
	}
}

// compressFile gzip-compresses src to dst.
func compressFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		return err
	}

	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		out.Close()
		return err
	}

	if err := gz.Close(); err != nil {
		out.Close()
		return err
	}

	return out.Close()
}
