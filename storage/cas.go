package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash computes the content hash for a chunk of data.
// Returns a lowercase hex-encoded SHA-256 digest.
func Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
