package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/logging"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var (
	serveFg   bool
	serveStop bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Gramaton server",
	Long: `Starts the Gramaton HTTP server daemon. By default, runs in the
background and exits immediately. Use --fg for foreground mode
(containers, debugging). Use --stop to shut down a running server.

The server loads the store into memory, serves the REST
API on the configured port (default 42982), and shuts down after
an idle timeout (default 30 minutes).`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().BoolVar(&serveFg, "fg", false, "run in foreground (for containers and debugging)")
	serveCmd.Flags().BoolVar(&serveStop, "stop", false, "stop a running server")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	if serveStop {
		return stopServer()
	}

	if !serveFg {
		return startBackground()
	}

	return startForeground()
}

// startForeground starts the server in the current process.
func startForeground() error {
	dir := configDir()

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	cfg := server.DefaultConfig()
	cfg.ConfigDir = dir
	cfg.StoreName = activeStoreName()

	// Override from engine config if server settings exist.
	engineCfg := eng.Config()
	if engineCfg.Server.Port > 0 {
		cfg.Port = engineCfg.Server.Port
	}
	// IdleTimeout: 0 means no timeout, >0 overrides default.
	// Only skip override when the YAML field was absent (defaults
	// set it to 30m, which we already have from DefaultConfig).
	cfg.IdleTimeout = engineCfg.Server.IdleTimeout

	// Background children (started by startBackground) set GRAMATON_BG=1.
	// Skip stderr logging to avoid duplicating lines into gramaton.stderr.
	foreground := os.Getenv("GRAMATON_BG") == ""
	logger, logWriter, err := logging.New(engineCfg.Logging, dir, foreground)
	if err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	defer logWriter.Close()

	srv, err := server.New(eng, cfg, logger)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	return srv.Run()
}

// startBackground starts the server as a detached background process.
func startBackground() error {
	dir := configDir()

	// Check if already running.
	info, err := server.ReadServerInfo(dir)
	if err == nil && server.IsProcessAlive(info.PID) {
		fmt.Fprintf(os.Stderr, "server already running (pid %d, port %d)\n", info.PID, info.Port)
		return nil
	}

	// Clean up stale info.
	if err == nil {
		server.RemoveServerInfo(dir)
	}

	// Resolve current executable for the subprocess.
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	// Build command with --fg flag so the child runs in foreground.
	cmdArgs := []string{"serve", "--fg"}
	if cfgDir != "" {
		cmdArgs = append(cmdArgs, "--config-dir", cfgDir)
	}
	if name := activeStoreName(); name != "" {
		cmdArgs = append(cmdArgs, "--store", name)
	}

	child := exec.Command(executable, cmdArgs...)
	child.Stdout = nil

	// Redirect stderr to a separate file so panics are visible.
	// Set GRAMATON_BG=1 so the child skips stderr logging (avoids
	// duplicating structured logs that already go to gramaton.log).
	stderrPath := filepath.Join(dir, "gramaton.stderr")
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		child.Stderr = nil
	} else {
		child.Stderr = stderrFile
	}
	child.Env = append(os.Environ(), "GRAMATON_BG=1")

	// Detach the child process.
	setSysProcAttr(child)

	if err := child.Start(); err != nil {
		if stderrFile != nil {
			stderrFile.Close()
		}
		return fmt.Errorf("start server: %w", err)
	}
	// Once Start succeeds the child has its own dup'd fd; the parent's
	// reference can be closed without affecting the child's writes.
	// Without this the file handle lived until GC, leaking an fd per
	// foreground spawn.
	if stderrFile != nil {
		stderrFile.Close()
	}

	// Wait for the server to be ready.
	if err := waitForServer(dir, 10*time.Second); err != nil {
		if tail := tailServerStderr(stderrPath, 2048); tail != "" {
			return fmt.Errorf("server failed to start: %w\n--- last stderr from child (%s) ---\n%s", err, stderrPath, tail)
		}
		return fmt.Errorf("server failed to start: %w", err)
	}

	info, _ = server.ReadServerInfo(dir)
	fmt.Fprintf(os.Stderr, "server started (pid %d, port %d)\n", info.PID, info.Port)
	return nil
}

// stopServer sends a shutdown request to a running server.
func stopServer() error {
	dir := configDir()

	info, err := server.ReadServerInfo(dir)
	if err != nil {
		return fmt.Errorf("no running server found")
	}

	if !server.IsProcessAlive(info.PID) {
		server.RemoveServerInfo(dir)
		return fmt.Errorf("server not running (stale info removed)")
	}

	url := fmt.Sprintf("http://%s:%d/v1/shutdown", info.Bind, info.Port)
	resp, err := httpPost(url, nil)
	if err != nil {
		return fmt.Errorf("shutdown request failed: %w", err)
	}
	resp.Body.Close()

	fmt.Fprintln(os.Stderr, "shutdown requested")
	return nil
}

// tailServerStderr returns up to maxBytes of trailing content from the
// child server's stderr file, trimmed to start at a line boundary.
// Returns "" if the file is missing, empty, or unreadable -- the caller
// falls back to the bare timeout error in that case.
func tailServerStderr(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return ""
	}
	buf := make([]byte, info.Size()-offset)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return ""
	}
	out := string(buf[:n])
	if offset > 0 {
		if nl := strings.IndexByte(out, '\n'); nl >= 0 && nl < len(out)-1 {
			out = out[nl+1:]
		}
	}
	return out
}

// waitForServer polls the lock-free /v1/health endpoint until the
// server is ready. Matches the endpoint the rest of the CLI uses for
// liveness probes (cli/client.go::pingServer) -- /v1/status takes an
// engine RLock, so on a busy server with a long-held write lock the
// status endpoint queues behind it while /v1/health responds promptly.
// Using /v1/health here avoids artificial startup-wait timeouts when
// the child is mid-init.
func waitForServer(cfgDir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := server.ReadServerInfo(cfgDir)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		url := fmt.Sprintf("http://%s:%d/v1/health", info.Bind, info.Port)
		resp, err := httpGet(url)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}
