package secret

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ResolveKey resolves an API key from explicit sources, in priority
// order:
//  1. keyFile  -- path to a file containing the key (api_key_file)
//  2. envName  -- name of an environment variable holding the key (api_key_env)
//  3. direct   -- the key value itself (api_key)
//
// Returns empty string if no source yields a non-empty value.
//
// Backward compatibility: pre-Wave-2 configs allowed envName to
// double as a direct key when it started with "sk-" (the value of
// envName WAS the key, not an env var name). This was an
// undocumented overload and a footgun -- a typo'd env var name like
// "sk-OPENAI_API_KEY" would be sent to the provider as the literal
// API key. Callers that hit this path now get a one-shot
// deprecation warning at slog.Warn level. Migrate to the explicit
// `direct` parameter (api_key config field).
func ResolveKey(keyFile, envName, direct string) string {
	// 1. Explicit file.
	if keyFile != "" {
		path := expandHome(keyFile)
		data, err := os.ReadFile(path)
		if err == nil {
			if key := strings.TrimSpace(string(data)); key != "" {
				return key
			}
		}
	}

	// 2. Environment variable name.
	if envName != "" {
		if val := os.Getenv(envName); val != "" {
			return val
		}
		// Deprecated overload: envName starts with "sk-" and looks
		// like a literal key, not an env var name. Continue to
		// honour it for backward compat, but warn once so users
		// migrate to the explicit `direct` parameter.
		if strings.HasPrefix(envName, "sk-") {
			warnLegacyDirectKey()
			return envName
		}
	}

	// 3. Explicit direct value.
	if direct != "" {
		return direct
	}

	return ""
}

var legacyDirectKeyWarnOnce sync.Once

func warnLegacyDirectKey() {
	legacyDirectKeyWarnOnce.Do(func() {
		slog.Warn("api_key_env contains a literal key (starts with sk-); this overload is deprecated",
			"component", "secret",
			"hint", "move the key to the explicit api_key field, or to a file referenced by api_key_file")
	})
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
