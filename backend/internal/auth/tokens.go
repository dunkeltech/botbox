// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GenerateOpaqueToken creates a cryptographically random URL-safe token with
// prefix and returns both the plaintext token and its SHA-256 digest for storage.
func GenerateOpaqueToken(prefix string, numBytes int) (plain string, digest []byte, err error) {
	if numBytes < 16 {
		numBytes = 16
	}
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate opaque token: %w", err)
	}
	plain = prefix + base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken computes the SHA-256 digest of a plaintext refresh or invitation token.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
