package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRemoteServer(t *testing.T) {
	base := func() Config {
		c := Defaults()
		c.Server.Remote.Enabled = true
		c.Server.Remote.Token = "tok"
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"no token", func(c *Config) { c.Server.Remote.Token = "" }, "token source"},
		{"token via file ok", func(c *Config) {
			c.Server.Remote.Token = ""
			c.Server.Remote.TokenFile = "/x/remote.token"
		}, ""},
		{"bad bind", func(c *Config) { c.Server.Remote.BindAddr = "not-an-ip" }, "bind_addr"},
		{"bind ip ok", func(c *Config) { c.Server.Remote.BindAddr = "192.168.1.10" }, ""},
		{"port collision", func(c *Config) {
			c.Server.Port = DefaultRemotePort
		}, "collides"},
		{"port out of range", func(c *Config) { c.Server.Remote.Port = 70000 }, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := Validate(&c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRemoteClient(t *testing.T) {
	base := func() Config {
		c := Defaults()
		c.Remote.URL = "https://host:42983"
		c.Remote.Pin = "sha256:" + strings.Repeat("a", 64)
		c.Remote.Token = "tok"
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"http rejected", func(c *Config) { c.Remote.URL = "http://host:42983" }, "https"},
		{"bad pin", func(c *Config) { c.Remote.Pin = "sha256:xyz" }, "pin"},
		{"uppercase pin ok", func(c *Config) { c.Remote.Pin = "SHA256:" + strings.Repeat("A", 64) }, ""},
		{"no token", func(c *Config) { c.Remote.Token = "" }, "token source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := Validate(&c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestServerRemoteRoundTrip pins that the host-side block survives
// Save/Load with the path/bind fields trimmed.
func TestServerRemoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c := Defaults()
	c.Server.Remote.Enabled = true
	c.Server.Remote.BindAddr = " 192.168.1.5 "
	c.Server.Remote.TokenFile = " /x/remote.token "
	c.Server.Remote.AdminOps = true
	if err := Save(c, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Server.Remote.BindAddr != "192.168.1.5" {
		t.Errorf("bind_addr = %q, want trimmed", got.Server.Remote.BindAddr)
	}
	if got.Server.Remote.TokenFile != "/x/remote.token" {
		t.Errorf("token_file = %q, want trimmed", got.Server.Remote.TokenFile)
	}
	if !got.Server.Remote.AdminOps {
		t.Error("admin_ops lost in round-trip")
	}
}

// TestClientRemoteRoundTrip pins the client-side block and that the
// inline token is NOT whitespace-trimmed (opaque secret) while the
// URL is.
func TestClientRemoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c := Defaults()
	c.Remote.URL = " https://host:42983 "
	c.Remote.Pin = "sha256:" + strings.Repeat("a", 64)
	c.Remote.Token = "  padded-secret  "
	if err := Save(c, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Remote.URL != "https://host:42983" {
		t.Errorf("url = %q, want trimmed", got.Remote.URL)
	}
	if got.Remote.Token != "  padded-secret  " {
		t.Errorf("inline token = %q, want untrimmed (opaque secret)", got.Remote.Token)
	}
}

// TestRemoteHostAndClientMutuallyExclusive pins that one config
// cannot both host and be a client.
func TestRemoteHostAndClientMutuallyExclusive(t *testing.T) {
	c := Defaults()
	c.Server.Remote.Enabled = true
	c.Server.Remote.Token = "t"
	c.Remote.URL = "https://host:42983"
	c.Remote.Pin = "sha256:" + strings.Repeat("a", 64)
	c.Remote.Token = "t"
	if err := Validate(&c); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutually-exclusive rejection", err)
	}
}

// TestRemoteOmittedWhenUnset pins that a default config renders no
// remote blocks, so existing config.yaml files are unchanged.
func TestRemoteOmittedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(Defaults(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\nremote:") {
		t.Fatal("default config should not render a top-level remote block")
	}
}
