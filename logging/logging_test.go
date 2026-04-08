package logging

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestNewCreatesLogFile(t *testing.T) {
	dir := t.TempDir()
	cfg := config.LoggingConfig{Level: "info", MaxSizeMB: 512, RotateSizeMB: 50}

	logger, rw, err := New(cfg, dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rw.Close()

	logger.Info("test message", "key", "value")

	// Verify log file exists.
	logPath := filepath.Join(dir, "gramaton.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("log file is empty after writing")
	}

	// Verify JSON format.
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"msg":"test message"`) {
		t.Fatalf("expected JSON log, got: %s", data)
	}
	if !strings.Contains(string(data), `"key":"value"`) {
		t.Fatalf("expected structured field, got: %s", data)
	}
}

func TestLogLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	cfg := config.LoggingConfig{Level: "warn", MaxSizeMB: 512, RotateSizeMB: 50}

	logger, rw, err := New(cfg, dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rw.Close()

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	data, _ := os.ReadFile(filepath.Join(dir, "gramaton.log"))
	s := string(data)

	if strings.Contains(s, "debug message") {
		t.Fatal("debug should be filtered at warn level")
	}
	if strings.Contains(s, "info message") {
		t.Fatal("info should be filtered at warn level")
	}
	if !strings.Contains(s, "warn message") {
		t.Fatal("warn should be logged at warn level")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		level string
	}{
		{"debug", "DEBUG"},
		{"info", "INFO"},
		{"warn", "WARN"},
		{"warning", "WARN"},
		{"error", "ERROR"},
		{"unknown", "INFO"},
		{"", "INFO"},
	}

	for _, tt := range tests {
		l := parseLevel(tt.input)
		if l.String() != tt.level {
			t.Errorf("parseLevel(%q) = %s, want %s", tt.input, l.String(), tt.level)
		}
	}
}

func TestRotatingWriterRotatesAtSize(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Rotate at 100 bytes, budget 10KB.
	rw, err := NewRotatingWriter(logPath, 100, 10*1024)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer rw.Close()

	// Write enough to trigger rotation.
	for i := 0; i < 20; i++ {
		rw.Write([]byte("This is a line of log output that is fairly long\n"))
	}

	// Check that compressed file was created.
	matches, _ := filepath.Glob(filepath.Join(dir, "test.log.*.gz"))
	if len(matches) == 0 {
		t.Fatal("expected rotated .gz files")
	}

	// Verify the .gz file is valid gzip.
	f, _ := os.Open(matches[0])
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("invalid gzip: %v", err)
	}
	gz.Close()
}

func TestRotatingWriterBudgetEnforcement(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Rotate at 50 bytes, budget 200 bytes (will delete old files).
	rw, err := NewRotatingWriter(logPath, 50, 200)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer rw.Close()

	// Write a lot to trigger multiple rotations.
	for i := 0; i < 100; i++ {
		rw.Write([]byte("Log line that will cause many rotations here\n"))
	}

	// Check total size of compressed files is under budget.
	matches, _ := filepath.Glob(filepath.Join(dir, "test.log.*.gz"))
	var totalSize int64
	for _, m := range matches {
		info, _ := os.Stat(m)
		if info != nil {
			totalSize += info.Size()
		}
	}

	if totalSize > 200 {
		t.Fatalf("total compressed size %d exceeds budget 200", totalSize)
	}
}

func TestRotatingWriterNextIndex(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	rw, _ := NewRotatingWriter(logPath, 1024, 10*1024)
	defer rw.Close()

	// First index should be 1.
	idx := rw.nextIndex()
	if idx != 1 {
		t.Fatalf("expected index 1, got %d", idx)
	}

	// Create a fake .gz file.
	os.WriteFile(filepath.Join(dir, "test.log.1.gz"), []byte("fake"), 0o600)

	idx = rw.nextIndex()
	if idx != 2 {
		t.Fatalf("expected index 2, got %d", idx)
	}
}
