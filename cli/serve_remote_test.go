package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/tlscert"
)

func TestResolveRemoteRuntimeFailClosed(t *testing.T) {
	dir := t.TempDir()

	t.Run("disabled is zero", func(t *testing.T) {
		rt, err := resolveRemoteRuntime(config.ServerConfig{}, dir)
		if err != nil || rt.Enabled {
			t.Fatalf("disabled remote should be zero-value, got %+v err=%v", rt, err)
		}
	})

	t.Run("enabled without token fails", func(t *testing.T) {
		sc := config.ServerConfig{Remote: config.RemoteServerConfig{Enabled: true}}
		_, err := resolveRemoteRuntime(sc, dir)
		if err == nil || !strings.Contains(err.Error(), "token") {
			t.Fatalf("expected token failure, got %v", err)
		}
	})

	t.Run("enabled with token but no cert fails", func(t *testing.T) {
		sc := config.ServerConfig{Remote: config.RemoteServerConfig{Enabled: true, Token: "tok"}}
		_, err := resolveRemoteRuntime(sc, dir)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("expected certificate failure, got %v", err)
		}
	})

	t.Run("enabled with token and generated cert resolves", func(t *testing.T) {
		storeDir := t.TempDir()
		if _, err := tlscert.Generate(remoteTLSDir(storeDir), []string{"127.0.0.1"}, tlscert.GenerateOptions{}); err != nil {
			t.Fatalf("gen cert: %v", err)
		}
		sc := config.ServerConfig{Remote: config.RemoteServerConfig{Enabled: true, Token: "tok", AdminOps: true}}
		rt, err := resolveRemoteRuntime(sc, storeDir)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !rt.Enabled || rt.Token != "tok" || !rt.AdminOps {
			t.Fatalf("runtime = %+v, want enabled+token+adminops", rt)
		}
		if _, err := os.Stat(rt.CertFile); err != nil {
			t.Fatalf("cert path %q not resolved: %v", rt.CertFile, err)
		}
	})

	t.Run("BYO cert path is honored", func(t *testing.T) {
		storeDir := t.TempDir()
		byoDir := t.TempDir()
		res, err := tlscert.Generate(byoDir, []string{"127.0.0.1"}, tlscert.GenerateOptions{})
		if err != nil {
			t.Fatalf("gen: %v", err)
		}
		sc := config.ServerConfig{
			TLS:    config.ServerTLSConfig{CertFile: res.CertPath, KeyFile: res.KeyPath},
			Remote: config.RemoteServerConfig{Enabled: true, Token: "tok"},
		}
		rt, err := resolveRemoteRuntime(sc, storeDir)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if rt.CertFile != res.CertPath {
			t.Fatalf("cert = %q, want BYO %q", rt.CertFile, res.CertPath)
		}
	})
}
