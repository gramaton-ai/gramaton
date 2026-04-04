package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brandonlattin/gramaton/core"
)

// ServerInfo is written to server.json for CLI discovery.
type ServerInfo struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Bind      string    `json:"bind"`
	StartedAt time.Time `json:"started_at"`
	StoreDir  string    `json:"store_dir"`
	Version   string    `json:"version"`
}

// serverInfoPath returns the path to server.json in the config dir.
func (s *Server) serverInfoPath() string {
	return filepath.Join(s.cfg.ConfigDir, "server.json")
}

// writeServerInfo writes the server info file for CLI discovery.
func (s *Server) writeServerInfo() error {
	info := ServerInfo{
		PID:       os.Getpid(),
		Port:      s.cfg.Port,
		Bind:      s.cfg.Bind,
		StartedAt: time.Now().UTC(),
		StoreDir:  s.engine.Config().DataDir,
		Version:   "0.2.0",
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server info: %w", err)
	}

	return core.AtomicWriteFile(s.serverInfoPath(), data, 0o600)
}

// removeServerInfo deletes the server info file on shutdown.
func (s *Server) removeServerInfo() {
	_ = os.Remove(s.serverInfoPath())
}

// ReadServerInfo reads the server info file from a config directory.
// Returns the info and nil error if the file exists and is valid.
func ReadServerInfo(cfgDir string) (*ServerInfo, error) {
	path := filepath.Join(cfgDir, "server.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var info ServerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse server info: %w", err)
	}

	return &info, nil
}

// RemoveServerInfo removes the server info file from a config directory.
func RemoveServerInfo(cfgDir string) {
	_ = os.Remove(filepath.Join(cfgDir, "server.json"))
}

// IsProcessAlive checks if a process with the given PID exists.
func IsProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check.
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
