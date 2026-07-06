package tlscert

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustGenerate(t *testing.T, dir string, hosts []string, opts GenerateOptions) Result {
	t.Helper()
	res, err := Generate(dir, hosts, opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return res
}

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("cert file is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func TestGenerateCreatesPinnedP256Keypair(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, []string{"localhost", "127.0.0.1", "gramaton.local", "192.168.1.10"}, GenerateOptions{})

	cert := parseCert(t, res.CertPath)
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("algorithm = %v, want ECDSA", cert.PublicKeyAlgorithm)
	}
	if len(cert.DNSNames) != 2 || cert.DNSNames[0] != "localhost" || cert.DNSNames[1] != "gramaton.local" {
		t.Fatalf("DNS SANs = %v, want [localhost gramaton.local]", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 2 {
		t.Fatalf("IP SANs = %v, want two entries", cert.IPAddresses)
	}
	if !cert.IsCA {
		t.Fatal("cert must be its own trust anchor (IsCA) for platform-trust clients")
	}
	if got := time.Until(cert.NotAfter); got < 9*365*24*time.Hour {
		t.Fatalf("validity %v, want ~10 years", got)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(res.Fingerprint) {
		t.Fatalf("fingerprint %q not in sha256:<hex> form", res.Fingerprint)
	}

	if runtime.GOOS != "windows" {
		for _, p := range []string{res.CertPath, res.KeyPath} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s perms = %o, want 0600", p, info.Mode().Perm())
			}
		}
	}
}

func TestGenerateRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	first := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})

	_, err := Generate(dir, []string{"localhost"}, GenerateOptions{})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Generate = %v, want ErrExists", err)
	}
	// The refusal must not have touched the originals.
	if got, err := LoadFingerprint(first.CertPath); err != nil || got != first.Fingerprint {
		t.Fatalf("original cert changed after refused overwrite: %q/%v", got, err)
	}
}

func TestGenerateForceBacksUpWithISO8601Stamp(t *testing.T) {
	dir := t.TempDir()
	first := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})
	origCert, err := os.ReadFile(first.CertPath)
	if err != nil {
		t.Fatal(err)
	}

	stamp := time.Date(2026, 7, 5, 22, 10, 0, 0, time.UTC)
	second := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{Force: true, Now: stamp})

	if len(second.BackedUp) != 2 {
		t.Fatalf("backed up %v, want cert and key", second.BackedUp)
	}
	wantName := regexp.MustCompile(`\.(2026-07-05T22-10-00Z)\.bak$`)
	for _, bak := range second.BackedUp {
		if !wantName.MatchString(filepath.Base(bak)) {
			t.Fatalf("backup name %q lacks the ISO8601 stamp", bak)
		}
	}
	// Backup preserves the original bytes.
	bakCert := second.BackedUp[0]
	if !strings.Contains(bakCert, CertFileName) {
		bakCert = second.BackedUp[1]
	}
	got, err := os.ReadFile(bakCert)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(origCert) {
		t.Fatal("backup content differs from the original certificate")
	}
	// Rotation minted a new key.
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("force regeneration reused the old key")
	}
	// Same-second double rotation must refuse rather than clobber
	// the backup.
	if _, err := Generate(dir, []string{"localhost"}, GenerateOptions{Force: true, Now: stamp}); err == nil {
		t.Fatal("same-second forced rotation should refuse to overwrite the existing backup")
	}
}

func TestLoadFingerprintMatchesGenerate(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})
	got, err := LoadFingerprint(res.CertPath)
	if err != nil {
		t.Fatalf("LoadFingerprint: %v", err)
	}
	if got != res.Fingerprint {
		t.Fatalf("LoadFingerprint = %q, Generate said %q", got, res.Fingerprint)
	}
}

// TestSPKIFingerprintStableAcrossReissue pins the documented
// security property: the fingerprint hashes the SubjectPublicKeyInfo,
// not the whole certificate, so a cert re-issued on the SAME key
// (new serial, new validity window -- different cert.Raw) keeps the
// same pin. A fingerprint accidentally computed over cert.Raw would
// pass every self-consistency test but fail this one.
func TestSPKIFingerprintStableAcrossReissue(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})

	keyPEM, err := os.ReadFile(res.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(keyPEM)
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse generated key: %v", err)
	}
	key := keyAny.(*ecdsa.PrivateKey)

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(4242),
		Subject:      pkix.Name{CommonName: "gramaton-reissued"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("reissue: %v", err)
	}
	reissued, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if got := SPKIFingerprint(reissued); got != res.Fingerprint {
		t.Fatalf("reissued cert on the same key has fingerprint %q, want the stable %q", got, res.Fingerprint)
	}
}

func TestGenerateWithOrphanKeyFile(t *testing.T) {
	dir := t.TempDir()
	first := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})
	// Simulate a crashed prior run: only the key survives.
	if err := os.Remove(first.CertPath); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(dir, []string{"localhost"}, GenerateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("orphan key without force = %v, want ErrExists", err)
	}

	stamp := time.Date(2026, 7, 5, 23, 0, 0, 0, time.UTC)
	res, err := Generate(dir, []string{"localhost"}, GenerateOptions{Force: true, Now: stamp})
	if err != nil {
		t.Fatalf("forced regeneration over orphan: %v", err)
	}
	if len(res.BackedUp) != 1 || !strings.Contains(res.BackedUp[0], KeyFileName) {
		t.Fatalf("BackedUp = %v, want exactly the orphan key", res.BackedUp)
	}
	for _, p := range []string{res.CertPath, res.KeyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("regeneration did not restore %s: %v", p, err)
		}
	}
}

func TestGenerateNoSANs(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, nil, GenerateOptions{})
	cert := parseCert(t, res.CertPath)
	if len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Fatalf("SANs = %v/%v, want none", cert.DNSNames, cert.IPAddresses)
	}
}

func TestBackupMissingSource(t *testing.T) {
	if _, err := Backup(filepath.Join(t.TempDir(), "absent.pem"), time.Now()); err == nil {
		t.Fatal("backing up a missing file must error")
	}
}

func TestLoadFingerprintErrorBranches(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFingerprint(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("missing file must error")
	}
	res := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})
	_, err := LoadFingerprint(res.KeyPath)
	if err == nil || !strings.Contains(err.Error(), "not a PEM certificate") {
		t.Fatalf("key file as input = %v, want the not-a-certificate error", err)
	}
}

// TestVerifyPeerPinnedNormalizesPin pins the copy-paste tolerance: a
// bundle pin with surrounding whitespace or uppercase hex must still
// match, since a formatting artifact must never read as an identity
// change.
func TestVerifyPeerPinnedNormalizesPin(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, []string{"localhost"}, GenerateOptions{})
	certPEM, err := os.ReadFile(res.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)

	sloppy := "  " + strings.ToUpper(res.Fingerprint) + "\n"
	// ToUpper capitalizes the "SHA256:" prefix too; the normalizer
	// lowercases the whole pin, so this exercises both trim and fold.
	if err := VerifyPeerPinned(sloppy)([][]byte{block.Bytes}, nil); err != nil {
		t.Fatalf("sloppy-but-correct pin rejected: %v", err)
	}
}

func TestVerifyPeerPinnedRejectsGarbage(t *testing.T) {
	verify := VerifyPeerPinned("sha256:" + strings.Repeat("ab", 32))
	if err := verify(nil, nil); err == nil {
		t.Fatal("no certificate must be rejected")
	}
	if err := verify([][]byte{[]byte("not a certificate")}, nil); err == nil {
		t.Fatal("unparseable certificate must be rejected")
	}
}

// TestPinnedHandshakeEndToEnd proves the full loop with a real TLS
// handshake: a server running the generated cert, a client verifying
// only the pin. The right pin connects; a wrong pin fails in the
// handshake and the request never reaches the handler.
func TestPinnedHandshakeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	res := mustGenerate(t, dir, []string{"127.0.0.1"}, GenerateOptions{})

	serverCfg, err := ServerTLS(res.CertPath, res.KeyPath)
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	var handlerHits int
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerHits++
		_, _ = io.WriteString(w, "pinned ok")
	}))
	srv.TLS = serverCfg
	srv.StartTLS()
	defer srv.Close()

	pinnedClient := func(pin string) *http.Client {
		return &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				// Verification is replaced by the pin, not skipped:
				// VerifyPeerCertificate runs on every handshake.
				InsecureSkipVerify:    true,
				VerifyPeerCertificate: VerifyPeerPinned(pin),
				MinVersion:            tls.VersionTLS13,
			}},
		}
	}

	resp, err := pinnedClient(res.Fingerprint).Get(srv.URL)
	if err != nil {
		t.Fatalf("correct pin failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "pinned ok" {
		t.Fatalf("body = %q", body)
	}

	wrongPin := "sha256:" + strings.Repeat("00", 32)
	_, err = pinnedClient(wrongPin).Get(srv.URL)
	if err == nil {
		t.Fatal("wrong pin must fail the handshake")
	}
	// The mismatch error is read by an agent (tenet 12): it must name
	// the mismatch and tell the operator what to do about it.
	for _, want := range []string{"does not match pinned", "credentials bundle"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wrong-pin error %q lacks the remediation phrase %q", err, want)
		}
	}
	if handlerHits != 1 {
		t.Fatalf("handler hits = %d; the wrong-pin request must never reach the handler", handlerHits)
	}
}
