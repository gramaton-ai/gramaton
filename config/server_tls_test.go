package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateServerTLSRequiresBothOrNeither(t *testing.T) {
	cases := []struct {
		name      string
		cert, key string
		wantErr   bool
	}{
		{"neither", "", "", false},
		{"both", "/tmp/cert.pem", "/tmp/key.pem", false},
		{"cert only", "/tmp/cert.pem", "", true},
		{"key only", "", "/tmp/key.pem", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Server.TLS.CertFile = tc.cert
			cfg.Server.TLS.KeyFile = tc.key
			err := Validate(&cfg)
			if tc.wantErr && (err == nil || !strings.Contains(err.Error(), "server.tls")) {
				t.Fatalf("err = %v, want a server.tls validation error", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestServerTLSRoundTripAndTrim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := Defaults()
	cfg.Server.TLS.CertFile = "  /etc/gramaton/cert.pem "
	cfg.Server.TLS.KeyFile = "\t/etc/gramaton/key.pem"
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.TLS.CertFile != "/etc/gramaton/cert.pem" {
		t.Fatalf("cert_file = %q, want trimmed path", loaded.Server.TLS.CertFile)
	}
	if loaded.Server.TLS.KeyFile != "/etc/gramaton/key.pem" {
		t.Fatalf("key_file = %q, want trimmed path", loaded.Server.TLS.KeyFile)
	}
}

// TestServerTLSCommentRendersWhenSet is the positive complement of
// the omission test: a config that sets the pair renders the tls
// block with its registry comment.
func TestServerTLSCommentRendersWhenSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Defaults()
	cfg.Server.TLS.CertFile = "/etc/gramaton/cert.pem"
	cfg.Server.TLS.KeyFile = "/etc/gramaton/key.pem"
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(data)
	for _, want := range []string{"tls:", "cert_file:", "bring-your-own certificate"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config lacks %q:\n%s", want, rendered)
		}
	}
}

// TestServerTLSOmittedWhenUnset pins the render behavior existing
// users see: a default config gains no tls block on disk.
func TestServerTLSOmittedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(Defaults(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tls:") {
		t.Fatal("default config should not render an empty tls block")
	}
}
