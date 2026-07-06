package cli

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/gramaton-ai/gramaton/internal/tlscert"
	"github.com/spf13/cobra"
)

// credentialBundle is the copy-pasteable artifact `remote enable`
// mints and `remote add` consumes. It carries everything a client
// needs to reach the server securely and nothing it should not: the
// address(es), the certificate SPKI pin, and the shared token. It
// travels out-of-band (the user's choice of channel) -- Gramaton
// does not transmit it.
type credentialBundle struct {
	Version int      `json:"v"`
	URLs    []string `json:"urls"`
	Pin     string   `json:"pin"`
	Token   string   `json:"token"`
}

const (
	remoteTokenFileName = "remote.token"
	bundleVersion       = 1
)

var (
	remoteEnableForce    bool
	remoteEnableAdminOps bool
	remoteEnableBind     string
	remoteEnablePort     int
	remoteEnableHosts    []string
	remoteAddBundleFile  string
	remoteDisablePurge   bool
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Serve a store to other machines, or point this machine at one",
	Long: `Remote access lets one machine host a Gramaton store and others
reach it over the LAN with token auth and TLS.

On the host:   gramaton remote enable   (prints a credentials bundle)
On a client:   gramaton remote add       (paste the bundle)`,
}

var remoteEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Turn on remote access and print a credentials bundle",
	Long: `Generates a bearer token and a self-signed TLS certificate, enables
the remote listener in this store's config, and prints a credentials
bundle to share with client machines.

Existing token or certificate material is not overwritten without
--force; with --force the old files are backed up with a timestamp
before new ones are written.`,
	RunE: runRemoteEnable,
}

var remoteDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Turn off remote access on this host",
	Long: `Disables the remote listener in this store's config (the plain
loopback listener is unaffected). Token and certificate material are
kept by default so 'gramaton remote enable' can turn it back on without
re-issuing a bundle; pass --purge to delete them too.`,
	RunE: runRemoteDisable,
}

var remoteAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Point this machine at a remote Gramaton server",
	Long: `Consumes a credentials bundle (from 'gramaton remote enable' on the
host), verifies the server's identity against the pinned certificate,
and writes this machine's remote config so every command dials the
remote server.

The store's MCP entry is also registered with every detected AI tool
(the proxy transparently dials the remote at runtime), so an agent can
use the store right away. Pass --no-harness to skip that. Use --store
<name> to attach the remote as a named store alongside a local one.`,
	RunE: runRemoteAdd,
}

func init() {
	remoteEnableCmd.Flags().BoolVar(&remoteEnableForce, "force", false, "overwrite existing token/certificate (backs up the old ones first)")
	remoteEnableCmd.Flags().BoolVar(&remoteEnableAdminOps, "admin-ops", false, "allow authenticated remotes to run path-taking admin operations")
	remoteEnableCmd.Flags().StringVar(&remoteEnableBind, "bind", "", "address to bind (default: all interfaces)")
	remoteEnableCmd.Flags().IntVar(&remoteEnablePort, "port", 0, fmt.Sprintf("port to bind (default: %d)", config.DefaultRemotePort))
	remoteEnableCmd.Flags().StringSliceVar(&remoteEnableHosts, "host", nil, "hostname/IP clients will use to reach this server (repeatable; used as certificate SANs and bundle URLs)")

	remoteAddCmd.Flags().StringVar(&remoteAddBundleFile, "bundle", "", "read the credentials bundle from a file (- for stdin) instead of the interactive prompt")
	remoteAddCmd.Flags().BoolVar(&storeNoHarness, "no-harness", false, "skip registering the store's MCP entry with detected AI tools")
	remoteDisableCmd.Flags().BoolVar(&remoteDisablePurge, "purge", false, "also delete the token and certificate material")

	remoteCmd.AddCommand(remoteEnableCmd)
	remoteCmd.AddCommand(remoteDisableCmd)
	remoteCmd.AddCommand(remoteAddCmd)
	rootCmd.AddCommand(remoteCmd)
}

func runRemoteEnable(cmd *cobra.Command, args []string) error {
	if ep, err := remoteMode(); err != nil {
		return err
	} else if ep != nil {
		return fmt.Errorf("this machine is a remote client of %s; run `gramaton remote enable` on the host instead", ep.url)
	}

	dir := configDir()
	now := time.Now().UTC()

	// Mint the shared token (0600, backup-on-force).
	tokenPath := filepath.Join(dir, remoteTokenFileName)
	token, err := writeSecretFile(tokenPath, newToken(), remoteEnableForce, now)
	if err != nil {
		return err
	}

	// Generate the certificate. SANs default to loopback plus any
	// supplied hosts so clients reaching the box by IP or name verify.
	hosts := append([]string{"127.0.0.1", "localhost"}, remoteEnableHosts...)
	certRes, err := tlscert.Generate(remoteTLSDir(dir), hosts, tlscert.GenerateOptions{Force: remoteEnableForce, Now: now})
	if err != nil {
		return err
	}

	// Persist the enabled config, pointing token_file at the file we
	// just wrote. Load the store's OWN config file (not the
	// global+store merge) so a per-store enable does not bake global
	// settings into the store file -- same pattern as `gramaton
	// configure`.
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Server.Remote.Enabled = true
	cfg.Server.Remote.BindAddr = remoteEnableBind
	cfg.Server.Remote.Port = remoteEnablePort
	cfg.Server.Remote.TokenFile = tokenPath
	cfg.Server.Remote.AdminOps = remoteEnableAdminOps
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	port := remoteEnablePort
	if port == 0 {
		port = config.DefaultRemotePort
	}
	bundle := credentialBundle{
		Version: bundleVersion,
		URLs:    bundleURLs(remoteEnableHosts, port),
		Pin:     certRes.Fingerprint,
		Token:   token,
	}
	encoded, err := encodeBundle(bundle)
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Remote access enabled.")
	if len(certRes.BackedUp) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Backed up prior certificate material: %s\n", strings.Join(certRes.BackedUp, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nRestart the server for this to take effect: %s\n", restartHint())
	fmt.Fprintln(cmd.OutOrStdout(), "Run it as a managed service (launchd/systemd); remote-serving disables idle auto-shutdown.")
	fmt.Fprintln(cmd.OutOrStdout(), "\nShare this bundle with each client machine (out of band -- e.g. a password manager):")
	fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n\nOn the client run: gramaton remote add\n", encoded)
	return nil
}

// runRemoteDisable turns off the remote listener in this store's
// config. The plain loopback listener is unaffected. Token and cert
// material are kept by default (so a later `remote enable` reuses them
// and clients keep the same pin); --purge deletes them.
func runRemoteDisable(cmd *cobra.Command, args []string) error {
	if ep, err := remoteMode(); err != nil {
		return err
	} else if ep != nil {
		return fmt.Errorf("this machine is a remote client of %s; there is no remote host to disable here", ep.url)
	}

	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Server.Remote.Enabled && !remoteDisablePurge {
		fmt.Fprintln(cmd.OutOrStdout(), "Remote access is already disabled.")
		return nil
	}

	// Capture the configured token path before clearing it, so --purge
	// deletes the token where it actually lives, not a hardcoded default.
	tokenFile := cfg.Server.Remote.TokenFile
	cfg.Server.Remote.Enabled = false
	if remoteDisablePurge {
		cfg.Server.Remote.TokenFile = ""
		cfg.Server.Remote.BindAddr = ""
		cfg.Server.Remote.Port = 0
		cfg.Server.Remote.AdminOps = false
	}
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if remoteDisablePurge {
		if tokenFile == "" {
			tokenFile = filepath.Join(dir, remoteTokenFileName)
		}
		_ = os.Remove(tokenFile)
		_ = os.RemoveAll(remoteTLSDir(dir))
		fmt.Fprintln(cmd.OutOrStdout(), "Remote access disabled; token and certificate removed.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Remote access disabled (token and certificate kept; pass --purge to remove them).")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Restart the server for this to take effect: %s\n", restartHint())
	return nil
}

func runRemoteAdd(cmd *cobra.Command, args []string) error {
	raw, err := readBundleInput(cmd)
	if err != nil {
		return err
	}
	bundle, err := decodeBundle(raw)
	if err != nil {
		return err
	}
	if len(bundle.URLs) == 0 {
		return fmt.Errorf("bundle carries no server URL; regenerate it with `gramaton remote enable --host <address>`")
	}

	dir := configDir()
	// Refuse to point a store that owns local data at a remote: the
	// remote pointer would take over at runtime and the local data would
	// be silently stranded. A store is either local (owns data/) or a
	// remote client, never both. Re-pointing an existing remote store
	// (remote.url set, no data/) is still allowed -- data/ is the guard,
	// not remote.url. Checked before the network probe so it fails fast.
	if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
		display := "(default)"
		if n := activeStoreName(); n != "" {
			display = n
		}
		return fmt.Errorf("store %s has local data at %s; pointing it at a remote would leave that data unreachable. "+
			"To use a remote store alongside a local one, create it under a fresh name: gramaton --store <name> remote add",
			display, filepath.Join(dir, "data"))
	}

	// Verify the server BEFORE writing config: a pin-checked,
	// authenticated health probe proves the bundle points at the real
	// server and the token is accepted.
	url, err := verifyRemote(bundle)
	if err != nil {
		return err
	}

	tokenPath := filepath.Join(dir, remoteTokenFileName)
	if _, err := writeSecretFile(tokenPath, bundle.Token, true, time.Now().UTC()); err != nil {
		return err
	}

	// Load the store's OWN config (not the global+store merge) so a
	// per-store `remote add` does not bake global settings into the
	// store file.
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Remote.URL = url
	cfg.Remote.Pin = bundle.Pin
	cfg.Remote.TokenFile = tokenPath
	cfg.Remote.Token = ""
	cfg.Remote.TokenEnv = ""
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Connected to %s. This machine now uses the remote store.\n", url)

	// Register the store's MCP entry with detected AI tools so an agent
	// can reach the remote store immediately. The entry runs `gramaton
	// [--store <name>] mcp`, whose proxy transparently dials the remote
	// (cli/client.go serverURL -> remoteMode), so the same registration
	// works for a local or remote store. --no-harness skips it.
	if !storeNoHarness {
		rep := setup.SyncStoreHarness(cmd.Context(), storeHarnessBackend, activeStoreName(), setup.EntryPresent)
		printHarnessSummary(cmd.OutOrStdout(), rep)
	}
	return nil
}

// verifyRemote probes each candidate URL with the pinned TLS config
// and the bundle token, returning the first that answers /v1/health.
// Verifying up front turns a bad bundle into a clear error here
// rather than an opaque failure on the next unrelated command.
func verifyRemote(bundle credentialBundle) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify:    true, //nolint:gosec // pin-verified below
			VerifyPeerCertificate: tlscert.VerifyPeerPinned(bundle.Pin),
			MinVersion:            tls.VersionTLS13,
		}},
	}
	var lastErr error
	for _, url := range bundle.URLs {
		url = strings.TrimRight(url, "/")
		req, err := http.NewRequest(http.MethodGet, url+"/v1/health", nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+bundle.Token)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return url, nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("server at %s rejected the bundle token; the bundle may be stale", url)
		}
		lastErr = fmt.Errorf("server at %s returned HTTP %d", url, resp.StatusCode)
	}
	return "", fmt.Errorf("could not reach the server on any bundle URL: %w", lastErr)
}

// restartHint is the "restart the server" command line, qualified with
// --store for a named store so the user restarts the store they just
// changed rather than the default store's server.
func restartHint() string {
	if name := activeStoreName(); name != "" {
		return fmt.Sprintf("gramaton --store %s stop && gramaton --store %s serve", name, name)
	}
	return "gramaton stop && gramaton serve"
}

// newToken returns a fresh URL-safe random bearer secret.
func newToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf) // crypto/rand.Read never returns a short read
	return base64.RawURLEncoding.EncodeToString(buf)
}

// writeSecretFile writes content to path at 0600. Existing content is
// preserved unless force is set, in which case the prior file is
// backed up with an ISO8601 stamp first. Returns the content written.
func writeSecretFile(path, content string, force bool, now time.Time) (string, error) {
	if _, err := os.Stat(path); err == nil {
		if !force {
			return "", fmt.Errorf("%s already exists; pass --force to replace it (the old file is backed up first)", path)
		}
		if _, err := tlscert.Backup(path, now); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return content, nil
}

// bundleURLs turns supplied hosts into https URLs. When no host was
// given the operator must tell clients the address out of band; we
// still emit a loopback URL so a same-machine client (or a smoke
// test) has something to try.
func bundleURLs(hosts []string, port int) []string {
	if len(hosts) == 0 {
		return []string{fmt.Sprintf("https://127.0.0.1:%d", port)}
	}
	urls := make([]string, 0, len(hosts))
	for _, h := range hosts {
		urls = append(urls, fmt.Sprintf("https://%s", net.JoinHostPort(h, fmt.Sprint(port))))
	}
	return urls
}

// encodeBundle renders a bundle as a single base64 line so it
// survives copy-paste through chat and password managers intact.
func encodeBundle(b credentialBundle) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("encode bundle: %w", err)
	}
	return "gramaton-remote:" + base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeBundle parses the string encodeBundle produced.
func decodeBundle(s string) (credentialBundle, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "gramaton-remote:")
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return credentialBundle{}, fmt.Errorf("bundle is malformed (expected the line printed by `gramaton remote enable`): %w", err)
	}
	var b credentialBundle
	if err := json.Unmarshal(data, &b); err != nil {
		return credentialBundle{}, fmt.Errorf("bundle is malformed: %w", err)
	}
	if b.Version != bundleVersion {
		return credentialBundle{}, fmt.Errorf("bundle version %d is not supported by this gramaton (expected %d)", b.Version, bundleVersion)
	}
	if b.Token == "" || b.Pin == "" {
		return credentialBundle{}, fmt.Errorf("bundle is missing its token or pin")
	}
	return b, nil
}

// readBundleInput reads the bundle from --bundle (file or stdin) or,
// absent the flag, prompts on the terminal.
func readBundleInput(cmd *cobra.Command) (string, error) {
	switch remoteAddBundleFile {
	case "":
		fmt.Fprint(cmd.OutOrStdout(), "Paste the credentials bundle from `gramaton remote enable`: ")
		var line string
		if _, err := fmt.Fscanln(cmd.InOrStdin(), &line); err != nil {
			return "", fmt.Errorf("read bundle: %w", err)
		}
		return line, nil
	case "-":
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read bundle from stdin: %w", err)
		}
		return string(data), nil
	default:
		data, err := os.ReadFile(remoteAddBundleFile)
		if err != nil {
			return "", fmt.Errorf("read bundle file: %w", err)
		}
		return string(data), nil
	}
}
