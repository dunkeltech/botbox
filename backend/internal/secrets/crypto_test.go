package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, secrets.AES256KeySize)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random key: %v", err)
	}
	return b
}

func TestKeyRingEnvelopeRoundTripAndRotation(t *testing.T) {
	t.Parallel()

	k1 := randomKey(t)
	k2 := randomKey(t)

	ringV1, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": k1})
	if err != nil {
		t.Fatalf("NewKeyRing(v1): %v", err)
	}
	// Mutating caller slice must not affect the ring.
	k1Copy := slices.Clone(k1)
	clear(k1)

	aad := []byte("workspace:ws-1/bot:bot-1/name:GITHUB_TOKEN")
	secretVal := []byte("ghp_1234567890abcdefABCDEF9876543210")

	env1, err := ringV1.Encrypt(secretVal, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if env1.KeyID != "v1" {
		t.Fatalf("KeyID = %q, want v1", env1.KeyID)
	}
	if bytes.Contains(env1.Ciphertext, secretVal) || bytes.Contains(env1.WrappedDEK, secretVal) {
		t.Fatal("encrypted output contains plaintext secret")
	}

	got, err := ringV1.Decrypt(env1, aad)
	if err != nil {
		t.Fatalf("Decrypt(v1): %v", err)
	}
	if !bytes.Equal(got, secretVal) {
		t.Fatalf("decrypted plaintext mismatch: got %q", got)
	}

	// Verify nonce uniqueness across repeated encryptions of identical plaintext.
	env2, err := ringV1.Encrypt(secretVal, aad)
	if err != nil {
		t.Fatalf("second Encrypt: %v", err)
	}
	if bytes.Equal(env1.Ciphertext, env2.Ciphertext) || bytes.Equal(env1.WrappedDEK, env2.WrappedDEK) {
		t.Fatal("expected distinct nonces/ciphertexts across encryptions")
	}

	// Rotate to v2 keeping v1 for decrypt & rewrap.
	ringV2, err := secrets.NewKeyRing("v2", map[string][]byte{"v1": k1Copy, "v2": k2})
	if err != nil {
		t.Fatalf("NewKeyRing(v2): %v", err)
	}

	rewrapped, err := ringV2.RewrapDataKey(env1, aad)
	if err != nil {
		t.Fatalf("RewrapDataKey: %v", err)
	}
	if rewrapped.KeyID != "v2" {
		t.Fatalf("rewrapped.KeyID = %q, want v2", rewrapped.KeyID)
	}
	if !bytes.Equal(rewrapped.Ciphertext, env1.Ciphertext) {
		t.Fatal("RewrapDataKey must not alter payload Ciphertext")
	}
	if bytes.Equal(rewrapped.WrappedDEK, env1.WrappedDEK) {
		t.Fatal("RewrapDataKey must generate a new WrappedDEK")
	}

	// Ring with only v2 must now decrypt the rewrapped secret, and reject the un-rotated v1 envelope.
	ringOnlyV2, err := secrets.NewKeyRing("v2", map[string][]byte{"v2": k2})
	if err != nil {
		t.Fatalf("NewKeyRing(only v2): %v", err)
	}
	gotRotated, err := ringOnlyV2.Decrypt(rewrapped, aad)
	if err != nil {
		t.Fatalf("Decrypt(rewrapped with v2): %v", err)
	}
	if !bytes.Equal(gotRotated, secretVal) {
		t.Fatalf("rotated decrypted plaintext mismatch")
	}
	if _, err := ringOnlyV2.Decrypt(env1, aad); !errors.Is(err, secrets.ErrUnknownKeyID) {
		t.Fatalf("expected ErrUnknownKeyID for v1 envelope after v1 retired, got %v", err)
	}
}

func TestKeyRingNegativeCases(t *testing.T) {
	t.Parallel()

	k1 := randomKey(t)
	k2 := randomKey(t)
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": k1})
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	aad := []byte("ws-a/bot-a/MY_KEY")
	env, err := ring.Encrypt([]byte("super-secret-value-12345"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Wrong AAD (cross-workspace or cross-secret swap attempt) must fail authentication.
	if _, err := ring.Decrypt(env, []byte("ws-b/bot-b/MY_KEY")); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("wrong AAD error = %v, want ErrInvalidCiphertext", err)
	}

	// Tampered payload ciphertext.
	tamperedPayload := env
	tamperedPayload.Ciphertext = slices.Clone(env.Ciphertext)
	tamperedPayload.Ciphertext[len(tamperedPayload.Ciphertext)-1] ^= 0xff
	if _, err := ring.Decrypt(tamperedPayload, aad); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("tampered ciphertext error = %v, want ErrInvalidCiphertext", err)
	}

	// Tampered wrapped DEK.
	tamperedDEK := env
	tamperedDEK.WrappedDEK = slices.Clone(env.WrappedDEK)
	tamperedDEK.WrappedDEK[0] ^= 0xff
	if _, err := ring.Decrypt(tamperedDEK, aad); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("tampered wrapped DEK error = %v, want ErrInvalidCiphertext", err)
	}
	if _, err := ring.RewrapDataKey(tamperedDEK, aad); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("rewrap tampered DEK error = %v, want ErrInvalidCiphertext", err)
	}

	// Truncated blobs must never panic.
	truncated := secrets.EnvelopeCiphertext{KeyID: "v1", WrappedDEK: []byte{1, 2}, Ciphertext: []byte{3}}
	if _, err := ring.Decrypt(truncated, aad); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("truncated error = %v, want ErrInvalidCiphertext", err)
	}

	// Empty plaintext is rejected.
	if _, err := ring.Encrypt(nil, aad); !errors.Is(err, secrets.ErrEmptyPlaintext) {
		t.Fatalf("empty plaintext error = %v, want ErrEmptyPlaintext", err)
	}

	// Invalid KeyRing definitions.
	invalidRings := []struct {
		name      string
		primaryID string
		keys      map[string][]byte
	}{
		{name: "empty primary", primaryID: "", keys: map[string][]byte{"v1": k1}},
		{name: "missing primary", primaryID: "v2", keys: map[string][]byte{"v1": k1}},
		{name: "wrong key length", primaryID: "v1", keys: map[string][]byte{"v1": []byte("too-short")}},
		{name: "empty key map", primaryID: "v1", keys: map[string][]byte{}},
		{name: "whitespace key id", primaryID: "v1", keys: map[string][]byte{"v1 ": k1}},
	}
	for _, tc := range invalidRings {
		if _, err := secrets.NewKeyRing(tc.primaryID, tc.keys); !errors.Is(err, secrets.ErrInvalidKey) {
			t.Errorf("%s: got %v, want ErrInvalidKey", tc.name, err)
		}
	}

	// Wrong KEK with same key ID must fail Decrypt cleanly.
	wrongRing, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": k2})
	if err != nil {
		t.Fatalf("NewKeyRing(wrong): %v", err)
	}
	if _, err := wrongRing.Decrypt(env, aad); !errors.Is(err, secrets.ErrInvalidCiphertext) {
		t.Fatalf("wrong key Decrypt error = %v, want ErrInvalidCiphertext", err)
	}
}

func TestParseAndLoadOrGenerateKeyRing(t *testing.T) {
	t.Parallel()

	k1 := randomKey(t)
	k2 := randomKey(t)

	// Single raw hex key gets assigned v1.
	ringHex, err := secrets.ParseKeyRing(hex.EncodeToString(k1))
	if err != nil {
		t.Fatalf("ParseKeyRing(hex): %v", err)
	}
	if ringHex.PrimaryID() != "v1" {
		t.Fatalf("PrimaryID = %q, want v1", ringHex.PrimaryID())
	}

	// Multi-key ring: first entry is primary, older keys still decrypt.
	spec := "v2:" + base64.RawURLEncoding.EncodeToString(k2) + ", v1:" + base64.StdEncoding.EncodeToString(k1)
	ringMulti, err := secrets.ParseKeyRing(spec)
	if err != nil {
		t.Fatalf("ParseKeyRing(multi): %v", err)
	}
	if ringMulti.PrimaryID() != "v2" {
		t.Fatalf("PrimaryID = %q, want v2", ringMulti.PrimaryID())
	}
	envFromHex, err := ringHex.Encrypt([]byte("test-secret-material"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	plain, err := ringMulti.Decrypt(envFromHex, []byte("ctx"))
	if err != nil || string(plain) != "test-secret-material" {
		t.Fatalf("multi ring failed to decrypt v1 ciphertext: err=%v plain=%q", err, plain)
	}

	// Invalid specs.
	badSpecs := []string{
		"",
		"not-valid-key",
		hex.EncodeToString(k1) + "," + hex.EncodeToString(k2),
		"v1:" + base64.StdEncoding.EncodeToString(k1) + ",v1:" + base64.StdEncoding.EncodeToString(k2),
		":abc",
	}
	for _, bad := range badSpecs {
		if _, err := secrets.ParseKeyRing(bad); !errors.Is(err, secrets.ErrInvalidKey) {
			t.Errorf("ParseKeyRing(%q): got %v, want ErrInvalidKey", bad, err)
		}
	}

	// LoadOrGenerateKeyRing persists 0600 file once and reloads identically.
	keyFile := filepath.Join(t.TempDir(), "nested", "secrets.kek")
	ring1, generated1, err := secrets.LoadOrGenerateKeyRing("", keyFile)
	if err != nil {
		t.Fatalf("LoadOrGenerateKeyRing(create): %v", err)
	}
	if !generated1 {
		t.Fatal("expected generated=true on initial load")
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file permissions = %v, want 0600", info.Mode().Perm())
	}

	env, err := ring1.Encrypt([]byte("persisted-secret-payload"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	ring2, generated2, err := secrets.LoadOrGenerateKeyRing("", keyFile)
	if err != nil {
		t.Fatalf("LoadOrGenerateKeyRing(reload): %v", err)
	}
	if generated2 {
		t.Fatal("expected generated=false on subsequent load")
	}
	decrypted, err := ring2.Decrypt(env, []byte("ctx"))
	if err != nil || string(decrypted) != "persisted-secret-payload" {
		t.Fatalf("reloaded key ring failed to decrypt: err=%v", err)
	}
}

func TestMaskedPreview(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "short secret hides all characters", secret: "short123", want: "••••••••"},
		{name: "medium secret shows only last two", secret: "mediumkey123", want: "••••...23"},
		{name: "anthropic key", secret: "sk-ant-api03-1234567890abcdef9f2a", want: "sk-ant-...9f2a"}, //nolint:gosec // synthetic test fixture
		{name: "github token", secret: "ghp_1234567890abcdef8b1c", want: "ghp_...8b1c"},
		{name: "generic long token", secret: "BearerTokenWithoutDash998877aabb", want: "Bear...aabb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := secrets.MaskedPreview(tc.secret)
			if got != tc.want {
				t.Fatalf("MaskedPreview(%q) = %q, want %q", tc.secret, got, tc.want)
			}
			if strings.Contains(got, tc.secret) {
				t.Fatalf("MaskedPreview leaked full secret: %q", got)
			}
		})
	}
}
