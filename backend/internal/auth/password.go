// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// MinPasswordLength is the minimum accepted plaintext password length in bytes.
	MinPasswordLength = 12
	// MaxPasswordLength is the maximum accepted plaintext password length in bytes.
	MaxPasswordLength = 128
)

var (
	// ErrInvalidPasswordLength indicates a password shorter than MinPasswordLength or longer than MaxPasswordLength.
	ErrInvalidPasswordLength = errors.New("password must be between 12 and 128 bytes")
	// ErrInvalidPasswordHash indicates a malformed or unsupported PHC password hash string.
	ErrInvalidPasswordHash = errors.New("invalid password hash format")
	// ErrInvalidArgon2Params indicates invalid Argon2id configuration parameters.
	ErrInvalidArgon2Params = errors.New("invalid argon2id parameters")
)

// Argon2Params defines tunable Argon2id work parameters.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultArgon2Params returns the production Argon2id parameters from Issue #3
// (64 MiB memory, t=3 iterations, p=2 lanes, 16-byte salt, 32-byte key).
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLen:     16,
		KeyLen:      32,
	}
}

// Validate checks that Argon2id parameters fall within safe bounds.
func (p Argon2Params) Validate() error {
	if p.MemoryKiB < 1024 || p.Iterations < 1 || p.Parallelism < 1 || p.SaltLen < 8 || p.KeyLen < 16 {
		return ErrInvalidArgon2Params
	}
	return nil
}

// ValidatePassword checks plaintext password length bounds.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return ErrInvalidPasswordLength
	}
	return nil
}

// HashPassword hashes password with Argon2id and encodes the result as a PHC string.
func HashPassword(password string, params Argon2Params) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if err := params.Validate(); err != nil {
		return "", err
	}

	salt := make([]byte, params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLen)
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Key := base64.RawStdEncoding.EncodeToString(key)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.MemoryKiB,
		params.Iterations,
		params.Parallelism,
		b64Salt,
		b64Key,
	), nil
}

// VerifyPassword checks password against an Argon2id PHC string in constant time
// and reports whether the hash should be upgraded to match targetParams.
func VerifyPassword(password, encodedHash string, targetParams Argon2Params) (match bool, needsUpgrade bool, err error) {
	parsed, salt, expectedKey, err := decodeArgon2Hash(encodedHash)
	if err != nil {
		return false, false, err
	}

	actualKey := argon2.IDKey(
		[]byte(password),
		salt,
		parsed.Iterations,
		parsed.MemoryKiB,
		parsed.Parallelism,
		parsed.KeyLen,
	)

	if subtle.ConstantTimeCompare(actualKey, expectedKey) != 1 {
		return false, false, nil
	}

	upgrade := parsed.MemoryKiB != targetParams.MemoryKiB ||
		parsed.Iterations != targetParams.Iterations ||
		parsed.Parallelism != targetParams.Parallelism ||
		parsed.SaltLen != targetParams.SaltLen ||
		parsed.KeyLen != targetParams.KeyLen

	return true, upgrade, nil
}

func decodeArgon2Hash(encodedHash string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}

	params, err := parseArgon2ParamSection(parts[3])
	if err != nil {
		return Argon2Params{}, nil, nil, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return Argon2Params{}, nil, nil, ErrInvalidPasswordHash
	}

	// Safe conversions bounded by base64 decoded slice sizes <= practical token lengths.
	params.SaltLen = uint32(len(salt)) // #nosec G115
	params.KeyLen = uint32(len(key))   // #nosec G115
	return params, salt, key, nil
}

func parseArgon2ParamSection(section string) (Argon2Params, error) {
	var params Argon2Params
	for _, kv := range strings.Split(section, ",") {
		pair := strings.SplitN(kv, "=", 2)
		if len(pair) != 2 {
			return Argon2Params{}, ErrInvalidPasswordHash
		}
		v, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return Argon2Params{}, ErrInvalidPasswordHash
		}
		switch pair[0] {
		case "m":
			params.MemoryKiB = uint32(v)
		case "t":
			params.Iterations = uint32(v)
		case "p":
			if v == 0 || v > 255 {
				return Argon2Params{}, ErrInvalidPasswordHash
			}
			params.Parallelism = uint8(v)
		default:
			return Argon2Params{}, ErrInvalidPasswordHash
		}
	}
	if params.MemoryKiB == 0 || params.Iterations == 0 || params.Parallelism == 0 {
		return Argon2Params{}, ErrInvalidPasswordHash
	}
	return params, nil
}
