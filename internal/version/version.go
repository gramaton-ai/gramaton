// Package version provides build and store format version information.
// Build variables (Version, Commit, Date) are set via ldflags at build
// time. StoreFormatVersion is a compile-time constant that tracks the
// on-disk store format.
package version

import (
	"runtime/debug"
	"strings"
)

// Build-time variables, set via:
//
//	go build -ldflags "-X github.com/gramaton-ai/gramaton/internal/version.Version=0.2.0
//	  -X github.com/gramaton-ai/gramaton/internal/version.Commit=$(git rev-parse --short HEAD)
//	  -X github.com/gramaton-ai/gramaton/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// When ldflags aren't injected (e.g. `go install` from a module), the
// init() below fills the sentinels from the binary's BuildInfo so the
// reported version still reflects the source it was built from.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	Version, Commit, Date = resolveFallbacks(Version, Commit, Date, info)
}

// resolveFallbacks fills sentinel values ("dev", "unknown") from
// BuildInfo when ldflags weren't injected at build time. Explicit
// ldflags-set values always win.
func resolveFallbacks(version, commit, date string, info *debug.BuildInfo) (string, string, string) {
	if info == nil {
		return version, commit, date
	}
	if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if commit == "unknown" && len(s.Value) >= 7 {
				commit = s.Value[:7]
			}
		case "vcs.time":
			if date == "unknown" && s.Value != "" {
				date = s.Value
			}
		}
	}
	return version, commit, date
}

// StoreFormatVersion is the current on-disk store format version.
// Bump this when making breaking changes to the store layout,
// property conventions, edge semantics, or serialization format.
//
// History:
//
//	1 -- initial format (prolly trees, content-addressed chunks,
//	     property encoding, collection nodes with member_of edges)
//	2 -- D7 timestamp-indexed commits (commit_timestamps bbolt
//	     bucket populated on every Save); collection schema gains
//	     clear_mode and curation fields with explicit defaults on
//	     existing collections. Migration is manual via `gramaton
//	     migrate` -- engine refuses to boot on v1 stores.
const StoreFormatVersion = 2
