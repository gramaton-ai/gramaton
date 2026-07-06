package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/tlscert"
)

// newPinnedTestServer stands up a TLS server with a generated cert that
// answers 200 to any authenticated request, and returns a bundle
// pinned to its cert. Shared by the remote-add wiring tests.
func newPinnedTestServer(t *testing.T, token string) (*httptest.Server, credentialBundle) {
	t.Helper()
	dir := t.TempDir()
	cert, err := tlscert.Generate(dir, []string{"127.0.0.1"}, tlscert.GenerateOptions{})
	if err != nil {
		t.Fatalf("gen cert: %v", err)
	}
	tlsCfg, err := tlscert.ServerTLS(cert.CertPath, cert.KeyPath)
	if err != nil {
		t.Fatalf("server tls: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	bundle := credentialBundle{Version: bundleVersion, URLs: []string{srv.URL}, Pin: cert.Fingerprint, Token: token}
	return srv, bundle
}

// writeBundleFile encodes a bundle to a temp file and returns its path.
func writeBundleFile(t *testing.T, b credentialBundle) string {
	t.Helper()
	enc, err := encodeBundle(b)
	if err != nil {
		t.Fatalf("encode bundle: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle.txt")
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

func TestRemoteAddRegistersHarness(t *testing.T) {
	base := newFreezeTestBase(t)
	_, bundle := newPinnedTestServer(t, "correct-token")
	bundleFile := writeBundleFile(t, bundle)
	if err := os.MkdirAll(filepath.Join(base, "stores", "team"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	t.Cleanup(func() { remoteAddBundleFile = "" })

	if _, err := runCmd(t, "remote", "add", "--bundle", bundleFile, "--store", "team", "--config-dir", base); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	// The named remote store's MCP entry is registered so an agent can
	// reach it; the proxy transparently dials the remote at runtime.
	if !reflect.DeepEqual(fake.registered, []string{"Claude Code:team"}) {
		t.Errorf("registered = %v, want [Claude Code:team]", fake.registered)
	}
	// Config was written pointing at the remote.
	raw, err := os.ReadFile(filepath.Join(base, "stores", "team", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "url:") {
		t.Errorf("remote config not written: err=%v", err)
	}
}

func TestLoadRemoteEndpointDoesNotInheritGlobalRemote(t *testing.T) {
	// A remote DEFAULT store's remote.url lives in base/config.yaml,
	// which is the global fallback every named store merges over. A LOCAL
	// named store that omits its own remote block must NOT inherit it --
	// otherwise it would be silently routed to the remote and its local
	// data stranded. Pins the own-config remote resolution.
	base := t.TempDir()
	// Point baseConfigDir() at the test base so that a REGRESSION which
	// re-added the global fallback (LoadWithFallback(dir, baseConfigDir()))
	// would actually read base/config.yaml's remote.url and fail this
	// test -- otherwise baseConfigDir() resolves to the real ~/.gramaton
	// and the decoy below is never on the read path (a vacuous test).
	oldCfgDir := cfgDir
	cfgDir = base
	t.Cleanup(func() { cfgDir = oldCfgDir })

	// Decoy: a remote DEFAULT store's remote.url in the global config.
	pin := "sha256:" + strings.Repeat("a", 64)
	writeRemoteClientConfig(t, base, "https://remote-default:42983", pin, "tok")

	named := filepath.Join(base, "stores", "work")
	if err := os.MkdirAll(named, 0o700); err != nil {
		t.Fatal(err)
	}
	// Local named store: data_dir only, no remote block.
	if err := os.WriteFile(filepath.Join(named, "config.yaml"), []byte("data_dir: "+named+"/data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ep, _, err := loadRemoteEndpoint(named)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ep != nil {
		t.Fatalf("local named store inherited the default's remote.url (bleed): ep=%+v", ep)
	}
}

func TestRemoteAddRefusesLocalData(t *testing.T) {
	// The default store here has local data (newFreezeTestBase writes
	// base/data), so pointing it at a remote would strand that data.
	base := newFreezeTestBase(t)
	_, bundle := newPinnedTestServer(t, "tok")
	bundleFile := writeBundleFile(t, bundle)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	t.Cleanup(func() { remoteAddBundleFile = "" })

	_, err := runCmd(t, "remote", "add", "--bundle", bundleFile, "--config-dir", base)
	if err == nil || !strings.Contains(err.Error(), "has local data") {
		t.Fatalf("remote add over a store with local data must be refused, got: %v", err)
	}
	// Refused before any write or registration.
	if len(fake.registered) != 0 {
		t.Errorf("refused remote add must not register: %v", fake.registered)
	}
	raw, _ := os.ReadFile(filepath.Join(base, "config.yaml"))
	if strings.Contains(string(raw), "url: http") {
		t.Error("refused remote add must not write remote.url into the config")
	}
}

func TestRemoteAddBadBundleWritesNothing(t *testing.T) {
	// verify-before-commit: a bad token fails the probe, so no token
	// file, no config, and no harness registration are written. Uses a
	// fresh named store (no local data) to get past the local-data guard.
	base := newFreezeTestBase(t)
	_, bundle := newPinnedTestServer(t, "correct-token")
	bundle.Token = "wrong-token"
	bundleFile := writeBundleFile(t, bundle)
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	t.Cleanup(func() { remoteAddBundleFile = "" })

	_, err := runCmd(t, "remote", "add", "--bundle", bundleFile, "--store", "team", "--config-dir", base)
	if err == nil {
		t.Fatal("a rejected token must fail remote add")
	}
	if len(fake.registered) != 0 {
		t.Errorf("failed verify must not register a harness entry: %v", fake.registered)
	}
	if _, serr := os.Stat(filepath.Join(base, "stores", "team", "config.yaml")); serr == nil {
		t.Error("failed verify must not write the store config")
	}
	if _, serr := os.Stat(filepath.Join(base, "stores", "team", remoteTokenFileName)); serr == nil {
		t.Error("failed verify must not write the token file")
	}
}

func TestRemoteDisableAlreadyDisabled(t *testing.T) {
	base := newFreezeTestBase(t)
	storeDir := filepath.Join(base, "stores", "host2")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(storeDir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteDisablePurge = false })

	out, err := runCmd(t, "remote", "disable", "--store", "host2", "--config-dir", base)
	if err != nil {
		t.Fatalf("disable on an already-disabled store: %v", err)
	}
	if !strings.Contains(string(out), "already disabled") {
		t.Errorf("output = %q, want the already-disabled no-op message", out)
	}
}

func TestRemoteDisable(t *testing.T) {
	base := newFreezeTestBase(t)
	storeDir := filepath.Join(base, "stores", "host")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(storeDir, remoteTokenFileName)
	if err := os.WriteFile(tokenPath, []byte("hosttoken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Server.Remote.Enabled = true
	cfg.Server.Remote.TokenFile = tokenPath
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, filepath.Join(storeDir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteDisablePurge = false })

	// Disable, keeping material.
	if _, err := runCmd(t, "remote", "disable", "--store", "host", "--config-dir", base); err != nil {
		t.Fatalf("remote disable: %v", err)
	}
	got, err := config.Load(filepath.Join(storeDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Server.Remote.Enabled {
		t.Error("remote disable should clear server.remote.enabled")
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Error("remote disable without --purge should keep the token file")
	}

	// Purge removes the token material.
	if _, err := runCmd(t, "remote", "disable", "--purge", "--store", "host", "--config-dir", base); err != nil {
		t.Fatalf("remote disable --purge: %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Error("remote disable --purge should remove the token file")
	}
}

func TestRemoteAddNoHarnessSkips(t *testing.T) {
	base := newFreezeTestBase(t)
	_, bundle := newPinnedTestServer(t, "correct-token")
	bundleFile := writeBundleFile(t, bundle)
	if err := os.MkdirAll(filepath.Join(base, "stores", "team2"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &fakeStoreHarnessBackend{clients: oneClient()}
	withStoreHarness(t, fake)
	t.Cleanup(func() { remoteAddBundleFile = "" })

	if _, err := runCmd(t, "remote", "add", "--bundle", bundleFile, "--store", "team2", "--no-harness", "--config-dir", base); err != nil {
		t.Fatalf("remote add --no-harness: %v", err)
	}
	if len(fake.registered) != 0 {
		t.Errorf("--no-harness still registered: %v", fake.registered)
	}
}

func TestBundleRoundTrip(t *testing.T) {
	in := credentialBundle{
		Version: bundleVersion,
		URLs:    []string{"https://192.168.1.5:42983"},
		Pin:     "sha256:" + strings.Repeat("a", 64),
		Token:   "the-token",
	}
	encoded, err := encodeBundle(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(encoded, "gramaton-remote:") {
		t.Fatalf("encoded bundle missing scheme prefix: %q", encoded)
	}
	// Survives whitespace a paste might introduce.
	out, err := decodeBundle("  " + encoded + "\n")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Token != in.Token || out.Pin != in.Pin || len(out.URLs) != 1 || out.URLs[0] != in.URLs[0] {
		t.Fatalf("round-trip mismatch: %+v vs %+v", out, in)
	}
}

func TestDecodeBundleRejects(t *testing.T) {
	cases := map[string]string{
		"garbage":    "not-a-bundle",
		"empty":      "",
		"bad base64": "gramaton-remote:!!!!",
	}
	for name, s := range cases {
		if _, err := decodeBundle(s); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// Wrong version.
	future := credentialBundle{Version: 99, URLs: []string{"https://x"}, Pin: "p", Token: "t"}
	enc, _ := encodeBundle(future)
	if _, err := decodeBundle(enc); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("wrong version should be rejected, got %v", err)
	}
}

// TestVerifyRemotePinnedHandshake proves the client-side verify path
// end to end: a TLS server with a generated cert, and verifyRemote
// accepting only the matching pin + token. A wrong pin fails the
// handshake; a wrong token yields a clear 401 diagnosis.
func TestVerifyRemotePinnedHandshake(t *testing.T) {
	dir := t.TempDir()
	cert, err := tlscert.Generate(dir, []string{"127.0.0.1"}, tlscert.GenerateOptions{})
	if err != nil {
		t.Fatalf("gen cert: %v", err)
	}
	tlsCfg, err := tlscert.ServerTLS(cert.CertPath, cert.KeyPath)
	if err != nil {
		t.Fatalf("server tls: %v", err)
	}

	const token = "correct-token"
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	good := credentialBundle{Version: bundleVersion, URLs: []string{srv.URL}, Pin: cert.Fingerprint, Token: token}
	if url, err := verifyRemote(good); err != nil || url != strings.TrimRight(srv.URL, "/") {
		t.Fatalf("correct bundle failed: url=%q err=%v", url, err)
	}

	wrongPin := good
	wrongPin.Pin = "sha256:" + strings.Repeat("0", 64)
	if _, err := verifyRemote(wrongPin); err == nil {
		t.Fatal("wrong pin must fail the handshake")
	}

	wrongToken := good
	wrongToken.Token = "nope"
	if _, err := verifyRemote(wrongToken); err == nil || !strings.Contains(err.Error(), "rejected the bundle token") {
		t.Fatalf("wrong token should give a token-rejection error, got %v", err)
	}
}

// recordingRoundTripper captures the request the bearerRoundTripper
// forwards so the injected header can be asserted.
type recordingRoundTripper struct{ got *http.Request }

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
}

// writeRemoteClientConfig drops a config.yaml pointing at a remote
// server into dir, for loadRemoteEndpoint tests.
func writeRemoteClientConfig(t *testing.T, dir, url, pin, token string) {
	t.Helper()
	body := "data_dir: " + dir + "/data\n"
	if url != "" {
		body += "remote:\n  url: " + url + "\n  pin: " + pin + "\n  token: " + token + "\n"
	}
	if err := os.WriteFile(dir+"/config.yaml", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRemoteEndpoint(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)

	t.Run("local mode when no url", func(t *testing.T) {
		dir := t.TempDir()
		writeRemoteClientConfig(t, dir, "", "", "")
		ep, rt, err := loadRemoteEndpoint(dir)
		if err != nil || ep != nil || rt != nil {
			t.Fatalf("expected local mode, got ep=%v rt=%v err=%v", ep, rt, err)
		}
	})

	t.Run("resolves endpoint and trims url", func(t *testing.T) {
		dir := t.TempDir()
		writeRemoteClientConfig(t, dir, "https://host:42983/", pin, "tok")
		ep, rt, err := loadRemoteEndpoint(dir)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if ep == nil || ep.url != "https://host:42983" || ep.token != "tok" {
			t.Fatalf("endpoint = %+v, want trimmed url + token", ep)
		}
		if rt == nil {
			t.Fatal("expected a RoundTripper to install")
		}
	})

	t.Run("url without token errors", func(t *testing.T) {
		dir := t.TempDir()
		// A URL with a valid pin but no token source. Write directly
		// to bypass the config-time token check so we exercise
		// loadRemoteEndpoint's own guard.
		body := "data_dir: " + dir + "/data\nremote:\n  url: https://host:42983\n  pin: " + pin + "\n"
		if err := os.WriteFile(dir+"/config.yaml", []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// config.Validate rejects url-without-token at load, so
		// LoadWithFallback errors -> loadRemoteEndpoint falls open to
		// local mode. Either way it must NOT return a usable endpoint.
		ep, _, _ := loadRemoteEndpoint(dir)
		if ep != nil {
			t.Fatalf("expected no endpoint for url-without-token, got %+v", ep)
		}
	})

	t.Run("broken config falls open to local", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(dir+"/config.yaml", []byte("this: [is: not: valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		ep, _, err := loadRemoteEndpoint(dir)
		if err != nil || ep != nil {
			t.Fatalf("broken config must fall open to local (nil,nil), got ep=%v err=%v", ep, err)
		}
	})
}

func TestBearerRoundTripperInjectsToken(t *testing.T) {
	rec := &recordingRoundTripper{}
	rt := &bearerRoundTripper{token: "the-token", next: rec}
	req := httptest.NewRequest("GET", "https://host/v1/status", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "Bearer the-token" {
		t.Fatalf("Authorization = %q, want Bearer the-token", got)
	}
	// The original request must not have been mutated (net/http
	// RoundTripper contract).
	if req.Header.Get("Authorization") != "" {
		t.Fatal("bearerRoundTripper mutated the caller's request")
	}
}

func TestNewTokenIsRandomAndURLSafe(t *testing.T) {
	a, b := newToken(), newToken()
	if a == b {
		t.Fatal("two tokens collided")
	}
	if strings.ContainsAny(a, "+/=") {
		t.Fatalf("token is not URL-safe: %q", a)
	}
	if len(a) < 40 {
		t.Fatalf("token too short (%d chars) for 32 bytes of entropy", len(a))
	}
}

func TestWriteSecretFileForceBackup(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/remote.token"
	if _, err := writeSecretFile(path, "first", false, time.Now().UTC()); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Without force, refuse.
	if _, err := writeSecretFile(path, "second", false, time.Now().UTC()); err == nil {
		t.Fatal("second write without force must refuse")
	}
	// With force, back up and replace.
	stamp := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC)
	if _, err := writeSecretFile(path, "second", true, stamp); err != nil {
		t.Fatalf("forced write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var haveBak bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "2026-07-05T23-30-00Z.bak") {
			haveBak = true
		}
	}
	if !haveBak {
		t.Fatalf("forced overwrite did not leave a timestamped backup: %v", entries)
	}
}
