package version

import (
	"runtime/debug"
	"testing"
)

func TestResolveFallbacks_LdflagsWin(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
		},
	}
	v, c, d := resolveFallbacks("0.3.0-alpha.1", "6d38ed0", "2026-05-07T02:09:24Z", info)
	if v != "0.3.0-alpha.1" {
		t.Errorf("Version: ldflags should win, got %q", v)
	}
	if c != "6d38ed0" {
		t.Errorf("Commit: ldflags should win, got %q", c)
	}
	if d != "2026-05-07T02:09:24Z" {
		t.Errorf("Date: ldflags should win, got %q", d)
	}
}

func TestResolveFallbacks_BuildInfoFills(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
		},
	}
	v, c, d := resolveFallbacks("dev", "unknown", "unknown", info)
	if v != "0.5.0" {
		t.Errorf("Version: BuildInfo should fill, got %q (want 0.5.0)", v)
	}
	if c != "abcdef1" {
		t.Errorf("Commit: BuildInfo should fill (7-char prefix), got %q (want abcdef1)", c)
	}
	if d != "2026-01-01T00:00:00Z" {
		t.Errorf("Date: BuildInfo should fill, got %q", d)
	}
}

func TestResolveFallbacks_PartialBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
	}
	v, c, d := resolveFallbacks("dev", "unknown", "unknown", info)
	if v != "0.5.0" {
		t.Errorf("Version: should fill from Main.Version, got %q", v)
	}
	if c != "unknown" {
		t.Errorf("Commit: no VCS info should leave default, got %q", c)
	}
	if d != "unknown" {
		t.Errorf("Date: no VCS info should leave default, got %q", d)
	}
}

func TestResolveFallbacks_DevelVersionIgnored(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}
	v, _, _ := resolveFallbacks("dev", "unknown", "unknown", info)
	if v != "dev" {
		t.Errorf("Version: '(devel)' should be ignored, got %q", v)
	}
}

func TestResolveFallbacks_NilInfo(t *testing.T) {
	v, c, d := resolveFallbacks("dev", "unknown", "unknown", nil)
	if v != "dev" || c != "unknown" || d != "unknown" {
		t.Errorf("nil BuildInfo should pass through values unchanged, got (%q, %q, %q)", v, c, d)
	}
}

func TestResolveFallbacks_VersionLdflagsKeepsCommitFallback(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
		},
	}
	v, c, _ := resolveFallbacks("0.3.0-alpha.1", "unknown", "unknown", info)
	if v != "0.3.0-alpha.1" {
		t.Errorf("Version: ldflags-set should win, got %q", v)
	}
	if c != "abcdef1" {
		t.Errorf("Commit: should fall back to BuildInfo when sentinel, got %q", c)
	}
}
