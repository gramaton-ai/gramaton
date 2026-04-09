package secret

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveKey resolves an API key from multiple sources, in order:
//  1. File path (api_key_file config field) -- read key from file
//  2. Environment variable (api_key_env as env var name)
//  3. Direct value (api_key_env starts with "sk-")
//
// Returns empty string if no key is found.
func ResolveKey(keyFile, envNameOrKey string) string {
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
	if envNameOrKey != "" {
		if val := os.Getenv(envNameOrKey); val != "" {
			return val
		}
		// 3. Direct key value.
		if strings.HasPrefix(envNameOrKey, "sk-") {
			return envNameOrKey
		}
	}

	return ""
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
