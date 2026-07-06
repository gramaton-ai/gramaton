package cli

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
	"github.com/gramaton-ai/gramaton/internal/tlscert"
)

// remoteEndpoint is this machine's resolved pointer at a Gramaton
// server on another machine. Nil endpoint means local mode.
type remoteEndpoint struct {
	url   string
	token string
}

var (
	remoteOnce     sync.Once
	remoteResolved *remoteEndpoint
	remoteErr      error
)

// remoteMode resolves the top-level `remote:` config once per
// process. It returns a non-nil endpoint when remote.url is set, and
// installs the bearer-token + pinned-TLS RoundTripper on the three
// shared HTTP clients so every client request (CLI, hooks, MCP
// proxy) carries the credential and trusts the pinned certificate.
// Local mode returns (nil, nil).
func remoteMode() (*remoteEndpoint, error) {
	remoteOnce.Do(func() {
		remoteResolved, remoteErr = resolveRemote()
	})
	return remoteResolved, remoteErr
}

func resolveRemote() (*remoteEndpoint, error) {
	ep, rt, err := loadRemoteEndpoint(configDir())
	if err != nil || ep == nil {
		return ep, err
	}
	// Install the auth + pinned-TLS transport on every shared client
	// so all client traffic (CLI, hooks, MCP proxy) carries it.
	httpClient.Transport = rt
	slowClient.Transport = rt
	healthClient.Transport = rt
	return ep, nil
}

// loadRemoteEndpoint is the pure resolution step: it reads the store's
// OWN config under dir and, when a remote URL is set, returns the
// endpoint and the RoundTripper to install. It has no global side
// effects, so it is unit-testable. Returns (nil, nil, nil) for local
// mode.
func loadRemoteEndpoint(dir string) (*remoteEndpoint, http.RoundTripper, error) {
	// Remote-ness is a PER-STORE property, read from the store's OWN
	// config -- never the global fallback. base/config.yaml is both the
	// default store's config AND the fallback every named store merges
	// over, so a remote DEFAULT store's remote.url would otherwise bleed
	// into a LOCAL named store that omits its own remote block, silently
	// routing it to the remote and stranding its local data.
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		// Fall open to local mode on a broken config, matching
		// shouldAutoStart: a config that fails to parse must not
		// wedge every command before the user can fix it. A user who
		// is actually remote sees the error when the local path then
		// fails to reach a server, not on an unrelated read command
		// against a still-running local server.
		return nil, nil, nil
	}
	if cfg.Remote.URL == "" {
		return nil, nil, nil
	}

	token := secret.ResolveKey(cfg.Remote.TokenFile, cfg.Remote.TokenEnv, cfg.Remote.Token)
	if token == "" {
		return nil, nil, fmt.Errorf("remote.url is set but no token resolves (remote.token_file/token_env/token); run `gramaton remote add`")
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			// Verification is REPLACED by the SPKI pin, not skipped:
			// VerifyPeerPinned runs on every handshake. See
			// internal/tlscert.VerifyPeerPinned.
			InsecureSkipVerify:    true, //nolint:gosec // pin-verified below
			VerifyPeerCertificate: tlscert.VerifyPeerPinned(cfg.Remote.Pin),
			MinVersion:            tls.VersionTLS13,
		},
	}
	rt := &bearerRoundTripper{token: token, next: transport}
	return &remoteEndpoint{url: strings.TrimRight(cfg.Remote.URL, "/"), token: token}, rt, nil
}

// bearerRoundTripper attaches the remote bearer token to every
// outbound request. Installed only in remote mode, where all traffic
// targets the configured server, so unconditional injection is
// correct.
type bearerRoundTripper struct {
	token string
	next  http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone before mutating: RoundTrip must not modify the caller's
	// request (net/http contract).
	r2 := req.Clone(req.Context())
	r2.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r2)
}

// isRemoteStore reports whether the store rooted at dir is configured
// as a remote client (remote.url set). It reads the store's OWN config
// only (no global fallback) rather than going through remoteMode(), so
// callers on the stop path work regardless of the process-global remote
// resolution and stay unit-testable. A config that fails to load is
// treated as local -- the same fall-open posture as loadRemoteEndpoint.
func isRemoteStore(dir string) bool {
	// Own config only, matching loadRemoteEndpoint and storeRemoteURL:
	// remote-ness is per-store and must not be inherited from the global
	// config (see loadRemoteEndpoint for why the fallback would bleed).
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return false
	}
	return cfg.Remote.URL != ""
}

// guardLocalStore returns an error when the command needs direct
// access to the store files but this machine is in remote mode.
// backfill/repair/validate/serve open the bbolt store directly and
// cannot operate against a server on another machine.
func guardLocalStore(op string) error {
	ep, err := remoteMode()
	if err != nil {
		return err
	}
	if ep != nil {
		return fmt.Errorf("%s operates on local store files and is not available in remote mode (this machine points at %s)", op, ep.url)
	}
	return nil
}
