// Package secrets manages encrypted credentials, LLM base-URL brokering,
// and selective TLS-intercepting egress proxies outside bot sandboxes.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// AES256KeySize is the required byte length for KEKs and envelope DEKs.
const AES256KeySize = 32

const (
	gcmNonceSize       = 12
	dekAADPrefix       = "botbox-dek-v1:"
	payloadAADPrefix   = "botbox-val-v1:"
	minMaskLongSecret  = 16
	minMaskMediumLen   = 10
	maxAutoPrefixRunes = 7
)

var (
	// ErrInvalidKey indicates a missing, malformed, or wrong-length key-encryption key.
	ErrInvalidKey = errors.New("invalid secrets key")
	// ErrUnknownKeyID indicates an envelope ciphertext references a key ID not in the key ring.
	ErrUnknownKeyID = errors.New("unknown secrets key id")
	// ErrInvalidCiphertext indicates malformed, truncated, or unauthenticated ciphertext.
	ErrInvalidCiphertext = errors.New("invalid secret ciphertext")
	// ErrEmptyPlaintext rejects encrypting an empty secret value.
	ErrEmptyPlaintext = errors.New("secret plaintext cannot be empty")
)

// EnvelopeCiphertext stores an AES-256-GCM encrypted payload along with the
// encrypted data-encryption key (DEK) and the identifier of the wrapping KEK.
type EnvelopeCiphertext struct {
	KeyID      string
	WrappedDEK []byte
	Ciphertext []byte
}

// KeyRing holds versioned 32-byte key-encryption keys (KEKs) for envelope encryption
// and zero-downtime key rotation via RewrapDataKey.
type KeyRing struct {
	primaryID string
	keys      map[string][]byte
}

// NewKeyRing constructs an immutable KeyRing validated for 256-bit keys.
func NewKeyRing(primaryID string, keys map[string][]byte) (*KeyRing, error) {
	primaryID = strings.TrimSpace(primaryID)
	if primaryID == "" {
		return nil, fmt.Errorf("%w: primary key id is required", ErrInvalidKey)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: at least one key is required", ErrInvalidKey)
	}

	copied := make(map[string][]byte, len(keys))
	for id, raw := range keys {
		cleanID := strings.TrimSpace(id)
		if cleanID == "" || cleanID != id {
			return nil, fmt.Errorf("%w: key id %q contains invalid whitespace or is empty", ErrInvalidKey, id)
		}
		if len(raw) != AES256KeySize {
			return nil, fmt.Errorf("%w: key %q must be %d bytes, got %d", ErrInvalidKey, cleanID, AES256KeySize, len(raw))
		}
		dup := make([]byte, AES256KeySize)
		copy(dup, raw)
		copied[cleanID] = dup
	}

	if _, ok := copied[primaryID]; !ok {
		return nil, fmt.Errorf("%w: primary key id %q missing from keys", ErrInvalidKey, primaryID)
	}

	return &KeyRing{
		primaryID: primaryID,
		keys:      copied,
	}, nil
}

// ParseKeyRing parses a comma-separated list of `id:encodedKey` entries (first entry is primary)
// or a single raw base64/hex key (assigned key ID "v1").
func ParseKeyRing(spec string) (*KeyRing, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("%w: empty key specification", ErrInvalidKey)
	}

	parts := strings.Split(spec, ",")
	keys := make(map[string][]byte, len(parts))
	var primaryID string

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty key entry at index %d", ErrInvalidKey, i)
		}

		var keyID, encoded string
		if id, val, hasColon := strings.Cut(part, ":"); hasColon {
			keyID = strings.TrimSpace(id)
			encoded = strings.TrimSpace(val)
		} else {
			if len(parts) > 1 {
				return nil, fmt.Errorf("%w: multi-key ring entries must use id:key format", ErrInvalidKey)
			}
			keyID = "v1"
			encoded = part
		}

		if keyID == "" {
			return nil, fmt.Errorf("%w: empty key id at index %d", ErrInvalidKey, i)
		}
		if _, exists := keys[keyID]; exists {
			return nil, fmt.Errorf("%w: duplicate key id %q", ErrInvalidKey, keyID)
		}

		raw, err := decodeKeyMaterial(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q: %v", ErrInvalidKey, keyID, err)
		}
		keys[keyID] = raw
		if i == 0 {
			primaryID = keyID
		}
	}

	return NewKeyRing(primaryID, keys)
}

// LoadOrGenerateKeyRing loads a KeyRing from envSpec if non-empty, or reads/initializes
// a persistent single-key file at filePath with mode 0600. It returns true when a new key file was written.
func LoadOrGenerateKeyRing(envSpec, filePath string) (*KeyRing, bool, error) {
	if strings.TrimSpace(envSpec) != "" {
		ring, err := ParseKeyRing(envSpec)
		return ring, false, err
	}

	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, false, fmt.Errorf("%w: no key spec or key file path provided", ErrInvalidKey)
	}

	data, err := os.ReadFile(filePath) // #nosec G304 -- filePath is administrator-configured KEK path
	if err == nil {
		ring, parseErr := ParseKeyRing(string(data))
		return ring, false, parseErr
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read secrets key file %s: %w", filePath, err)
	}

	raw := make([]byte, AES256KeySize)
	if _, randErr := io.ReadFull(rand.Reader, raw); randErr != nil {
		return nil, false, fmt.Errorf("generate secrets key: %w", randErr)
	}
	spec := "v1:" + base64.StdEncoding.EncodeToString(raw)

	ring, err := ParseKeyRing(spec)
	if err != nil {
		return nil, false, err
	}

	if mkErr := os.MkdirAll(filepath.Dir(filePath), 0o700); mkErr != nil {
		return nil, false, fmt.Errorf("create secrets key directory: %w", mkErr)
	}
	if writeErr := os.WriteFile(filePath, []byte(spec+"\n"), 0o600); writeErr != nil {
		return nil, false, fmt.Errorf("write secrets key file %s: %w", filePath, writeErr)
	}
	return ring, true, nil
}

// PrimaryID returns the identifier of the active key used for new encryptions and re-wrapping.
func (r *KeyRing) PrimaryID() string {
	return r.primaryID
}

// KeyIDs returns the set of key IDs known to this ring.
func (r *KeyRing) KeyIDs() []string {
	ids := make([]string, 0, len(r.keys))
	for id := range maps.Keys(r.keys) {
		ids = append(ids, id)
	}
	return ids
}

// Encrypt generates a fresh per-secret 256-bit DEK, encrypts plaintext using AES-256-GCM
// bound to aad, and wraps the DEK under the primary KEK bound to the same aad.
func (r *KeyRing) Encrypt(plaintext, aad []byte) (EnvelopeCiphertext, error) {
	if len(plaintext) == 0 {
		return EnvelopeCiphertext{}, ErrEmptyPlaintext
	}

	kek, ok := r.keys[r.primaryID]
	if !ok {
		return EnvelopeCiphertext{}, fmt.Errorf("%w: %s", ErrUnknownKeyID, r.primaryID)
	}

	dek := make([]byte, AES256KeySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return EnvelopeCiphertext{}, fmt.Errorf("generate DEK: %w", err)
	}
	defer clear(dek)

	wrappedDEK, err := sealGCM(kek, dek, prefixedAAD(dekAADPrefix, aad))
	if err != nil {
		return EnvelopeCiphertext{}, fmt.Errorf("wrap DEK: %w", err)
	}

	sealedPayload, err := sealGCM(dek, plaintext, prefixedAAD(payloadAADPrefix, aad))
	if err != nil {
		return EnvelopeCiphertext{}, fmt.Errorf("encrypt payload: %w", err)
	}

	return EnvelopeCiphertext{
		KeyID:      r.primaryID,
		WrappedDEK: wrappedDEK,
		Ciphertext: sealedPayload,
	}, nil
}

// Decrypt unwraps the envelope's DEK using KeyID and decrypts the secret payload.
// Caller must keep aad identical to what was supplied at Encrypt time.
func (r *KeyRing) Decrypt(env EnvelopeCiphertext, aad []byte) ([]byte, error) {
	kek, ok := r.keys[env.KeyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKeyID, env.KeyID)
	}

	dek, err := openGCM(kek, env.WrappedDEK, prefixedAAD(dekAADPrefix, aad))
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap DEK", ErrInvalidCiphertext)
	}
	defer clear(dek)

	if len(dek) != AES256KeySize {
		return nil, fmt.Errorf("%w: unexpected DEK length", ErrInvalidCiphertext)
	}

	plaintext, err := openGCM(dek, env.Ciphertext, prefixedAAD(payloadAADPrefix, aad))
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt payload", ErrInvalidCiphertext)
	}
	return plaintext, nil
}

// RewrapDataKey rotates the envelope's KEK without decrypting or modifying Ciphertext.
// A fresh wrapping nonce is always generated even if the envelope already uses PrimaryID.
func (r *KeyRing) RewrapDataKey(env EnvelopeCiphertext, aad []byte) (EnvelopeCiphertext, error) {
	oldKEK, ok := r.keys[env.KeyID]
	if !ok {
		return EnvelopeCiphertext{}, fmt.Errorf("%w: %q", ErrUnknownKeyID, env.KeyID)
	}
	newKEK, ok := r.keys[r.primaryID]
	if !ok {
		return EnvelopeCiphertext{}, fmt.Errorf("%w: %q", ErrUnknownKeyID, r.primaryID)
	}

	dek, err := openGCM(oldKEK, env.WrappedDEK, prefixedAAD(dekAADPrefix, aad))
	if err != nil {
		return EnvelopeCiphertext{}, fmt.Errorf("%w: unwrap DEK", ErrInvalidCiphertext)
	}
	defer clear(dek)

	if len(dek) != AES256KeySize {
		return EnvelopeCiphertext{}, fmt.Errorf("%w: unexpected DEK length", ErrInvalidCiphertext)
	}

	rewrapped, err := sealGCM(newKEK, dek, prefixedAAD(dekAADPrefix, aad))
	if err != nil {
		return EnvelopeCiphertext{}, fmt.Errorf("rewrap DEK: %w", err)
	}

	ciphertextCopy := make([]byte, len(env.Ciphertext))
	copy(ciphertextCopy, env.Ciphertext)

	return EnvelopeCiphertext{
		KeyID:      r.primaryID,
		WrappedDEK: rewrapped,
		Ciphertext: ciphertextCopy,
	}, nil
}

// MaskedPreview formats a write-only preview suitable for UI display and logs.
// Short secrets (< 10 runes) reveal zero characters so high-entropy guesses cannot reconstruct them.
func MaskedPreview(secret string) string {
	runes := []rune(secret)
	n := len(runes)
	if n < minMaskMediumLen {
		return "••••••••"
	}
	if n < minMaskLongSecret {
		return "••••..." + string(runes[n-2:])
	}

	// Preserve recognizable token prefixes such as "sk-ant-" or "ghp_" when safe.
	prefixLen := 4
	for idx := 2; idx < maxAutoPrefixRunes && idx < n/4; idx++ {
		if runes[idx] == '-' || runes[idx] == '_' {
			prefixLen = idx + 1
		}
	}
	return string(runes[:prefixLen]) + "..." + string(runes[n-4:])
}

func decodeKeyMaterial(encoded string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, fn := range decoders {
		if b, err := fn(encoded); err == nil && len(b) == AES256KeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("expected 32-byte key in base64 or hex")
}

func prefixedAAD(prefix string, aad []byte) []byte {
	out := make([]byte, len(prefix)+len(aad))
	copy(out, prefix)
	copy(out[len(prefix):], aad)
	return out
}

func sealGCM(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func openGCM(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcmNonceSize+gcm.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	nonce := sealed[:gcmNonceSize]
	ciphertext := sealed[gcmNonceSize:]
	return gcm.Open(nil, nonce, ciphertext, aad)
}
