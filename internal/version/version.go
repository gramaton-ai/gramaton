// Package version provides build and store format version information.
// Build variables (Version, Commit, Date) are set via ldflags at build
// time. StoreFormatVersion is a compile-time constant that tracks the
// on-disk store format.
package version

// Build-time variables, set via:
//
//	go build -ldflags "-X github.com/gramaton-ai/gramaton/internal/version.Version=0.2.0
//	  -X github.com/gramaton-ai/gramaton/internal/version.Commit=$(git rev-parse --short HEAD)
//	  -X github.com/gramaton-ai/gramaton/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// StoreFormatVersion is the current on-disk store format version.
// Bump this when making breaking changes to the store layout,
// property conventions, edge semantics, or serialization format.
//
// History:
//   1 -- initial format (prolly trees, content-addressed chunks,
//        property encoding, collection nodes with member_of edges)
//   2 -- D7 timestamp-indexed commits (commit_timestamps bbolt
//        bucket populated on every Save); collection schema gains
//        clear_mode and curation fields with explicit defaults on
//        existing collections. Migration is manual via `gramaton
//        migrate` -- engine refuses to boot on v1 stores.
const StoreFormatVersion = 2
