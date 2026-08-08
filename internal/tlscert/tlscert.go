// Package tlscert generates and verifies the self-signed certificate
// for Gramaton's remote TLS listener. Trust is pin-based: a client
// verifies the server's SPKI fingerprint carried out-of-band in the
// credentials bundle, not a CA chain, so certificates are long-lived
// and SAN churn (a DHCP address change) does not break verification.
// Standard library only.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CertFileName and KeyFileName are the fixed basenames Generate
// writes inside its target directory.
const (
	CertFileName = "cert.pem"
	KeyFileName  = "key.pem"
)

// backupStampLayout is ISO 8601 with dashes in place of colons so
// the stamp is filesystem-safe on every platform while staying
// human-readable (e.g. cert.pem.2026-07-05T22-10-00Z.bak).
const backupStampLayout = "2006-01-02T15-04-05Z"

// certValidity is deliberately long: the pin, not the expiry, is the
// trust anchor, and a short-lived cert would force bundle reissues
// on every rotation for no security gain.
const certValidity = 10 * 365 * 24 * time.Hour

// ErrExists reports that Generate found existing certificate
// material and Force was not set.
var ErrExists = errors.New("certificate material already exists")

// GenerateOptions tunes Generate.
type GenerateOptions struct {
	// Force overwrites existing cert/key files. Each existing file
	// is renamed to an ISO8601-stamped .bak first, never deleted.
	Force bool
	// Now overrides the clock (backup stamps, validity window).
	// Zero means time.Now.
	Now time.Time
}

// Result reports what Generate produced.
type Result struct {
	CertPath    string
	KeyPath     string
	Fingerprint string   // SPKI fingerprint, "sha256:<hex>"
	BackedUp    []string // stamped backups created under Force
}

// Generate creates an ECDSA P-256 self-signed server certificate in
// dir, writing cert.pem and key.pem at 0600 (dir created 0700).
// hosts become SANs: parseable IPs go to IPAddresses, everything
// else to DNSNames. If either output file already exists and
// opts.Force is false, Generate returns ErrExists and touches
// nothing; with Force it renames each existing file to
// <name>.<ISO8601>.bak before writing.
//
// P-256 over ed25519 because zero-install clients (a harness's own
// HTTP MCP stack) must accept this cert via platform trust, and
// P-256 is universally supported where ed25519 certs still are not.
func Generate(dir string, hosts []string, opts GenerateOptions) (Result, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create tls dir: %w", err)
	}
	// MkdirAll is a no-op on a pre-existing directory, so a dir
	// inherited with loose permissions would otherwise stay loose.
	if err := os.Chmod(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("tighten tls dir permissions: %w", err)
	}
	certPath := filepath.Join(dir, CertFileName)
	keyPath := filepath.Join(dir, KeyFileName)

	var existing []string
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		}
	}
	if len(existing) > 0 && !opts.Force {
		return Result{}, fmt.Errorf("%w in %s; pass force to overwrite (existing files are backed up first)", ErrExists, dir)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Result{}, fmt.Errorf("generate serial: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gramaton"},
		// NotBefore backdated an hour so a client with mild clock
		// skew accepts a just-minted cert.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// IsCA lets the self-signed cert act as its own trust anchor
		// for zero-install clients that import it into a CA pool
		// (NODE_EXTRA_CA_CERTS, OS keychain). Pinned gramaton clients
		// ignore chain building entirely. A stolen key could sign
		// other certs, but any compromise of a pinned key is already
		// total, so this grants nothing extra.
		IsCA: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return Result{}, fmt.Errorf("create certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return Result{}, fmt.Errorf("parse generated certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Result{}, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	// All new material is minted; only now touch existing files. A
	// failure before this point leaves the previous cert fully in
	// place, and a failure after it restores the backups, so the
	// canonical paths never end up empty because a rotation died
	// midway.
	var backups []backupPair
	for _, p := range existing {
		bak, err := Backup(p, now)
		if err != nil {
			return Result{}, restoreOnFailure(backups, err)
		}
		backups = append(backups, backupPair{orig: p, bak: bak})
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return Result{}, restoreOnFailure(backups, fmt.Errorf("write key: %w", err))
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		// Do not leave a fresh key with no matching cert: the next
		// un-forced Generate would refuse on the orphan.
		_ = os.Remove(keyPath)
		return Result{}, restoreOnFailure(backups, fmt.Errorf("write certificate: %w", err))
	}

	res := Result{
		CertPath:    certPath,
		KeyPath:     keyPath,
		Fingerprint: SPKIFingerprint(cert),
	}
	for _, b := range backups {
		res.BackedUp = append(res.BackedUp, b.bak)
	}
	return res, nil
}

// backupPair remembers where a live file was renamed to so a failed
// rotation can put it back.
type backupPair struct {
	orig, bak string
}

// restoreOnFailure renames stamped backups back to their canonical
// paths after a rotation step failed, so the caller is left with the
// previous working certificate instead of an orphaned directory. If
// a restore itself fails, the returned error names the backup path
// the operator must recover by hand.
func restoreOnFailure(backups []backupPair, cause error) error {
	for _, b := range backups {
		if err := os.Rename(b.bak, b.orig); err != nil {
			return fmt.Errorf("%w; ADDITIONALLY restoring %s from %s failed (%v) -- restore it by hand", cause, b.orig, b.bak, err)
		}
	}
	return cause
}

// Backup renames path to <path>.<ISO8601>.bak and returns the new
// path. It never deletes: a stamped backup that already exists (two
// rotations inside one second) is an error rather than an overwrite.
// Shared by certificate and token rotation.
func Backup(path string, now time.Time) (string, error) {
	bak := path + "." + now.UTC().Format(backupStampLayout) + ".bak"
	if _, err := os.Stat(bak); err == nil {
		return "", fmt.Errorf("backup target %s already exists; retry in a second", bak)
	}
	if err := os.Rename(path, bak); err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	return bak, nil
}

// SPKIFingerprint returns the SHA-256 of the certificate's
// SubjectPublicKeyInfo as "sha256:<hex>". Hashing the SPKI rather
// than the whole certificate keeps the pin stable across re-issued
// certificates that reuse the key.
func SPKIFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VerifyPeerPinned returns a tls.Config.VerifyPeerCertificate
// callback that accepts exactly the certificate whose SPKI
// fingerprint matches pin. Callers set InsecureSkipVerify: true
// alongside it -- chain and hostname verification are REPLACED by
// the pin, not skipped: the pin arrived out-of-band in the
// credentials bundle, so a matching key proves server identity with
// no CA and no dependence on SANs surviving DHCP churn.
func VerifyPeerPinned(pin string) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	// Normalize once: pins travel by copy-paste, and a trailing
	// newline or uppercase hex from an external tool must not read
	// as an identity change.
	pin = strings.ToLower(strings.TrimSpace(pin))
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("server presented no certificate")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse server certificate: %w", err)
		}
		got := SPKIFingerprint(leaf)
		if subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
			return fmt.Errorf("server certificate fingerprint %s does not match pinned %s: the server identity changed -- import a fresh credentials bundle", got, pin)
		}
		return nil
	}
}

// ServerTLS loads the certificate and key into a server-side
// tls.Config. TLS 1.3 only: every supported client (our pinned
// clients and current Node/Python MCP stacks) speaks it, and 1.3
// removes the downgrade surface a LAN listener does not need to
// carry.
func ServerTLS(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}
