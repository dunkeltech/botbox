// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// SetupManager holds the SHA-256 digest of the single-use first-run setup secret
// while the database has zero users, and clears it immediately on completion.
type SetupManager struct {
	mu     sync.RWMutex
	active bool
	digest [32]byte
	out    io.Writer
}

// NewSetupManager constructs a SetupManager that writes the one-time startup banner
// to out (defaulting to os.Stdout when out is nil).
func NewSetupManager(out io.Writer) *SetupManager {
	if out == nil {
		out = os.Stdout
	}
	return &SetupManager{out: out}
}

// EnsureSetupSecret activates first-run setup mode when userCount == 0 by storing
// only the SHA-256 digest of envSecret or a freshly generated 128-bit secret.
// Auto-generated secrets are printed once to the configured stdout writer and never
// emitted into structured slog attributes.
func (m *SetupManager) EnsureSetupSecret(_ context.Context, userCount int64, envSecret string, logger *slog.Logger) (string, bool, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if userCount > 0 {
		m.Clear()
		return "", false, nil
	}

	secret := strings.TrimSpace(envSecret)
	fromEnv := secret != ""
	if !fromEnv {
		var err error
		secret, err = GenerateSetupSecret()
		if err != nil {
			return "", false, err
		}
	}

	sum := sha256.Sum256([]byte(secret))
	m.mu.Lock()
	m.digest = sum
	m.active = true
	m.mu.Unlock()

	if fromEnv {
		logger.Info("database has 0 users; entering first-run setup using configured BOTBOX_SETUP_SECRET")
	} else {
		logger.Info("database has 0 users; entering first-run setup")
		// The operator needs the generated secret to unlock first-run setup.
		_, _ = fmt.Fprintf(m.out, "\nONE-TIME SETUP SECRET: %s\n\n", secret)
	}

	return secret, true, nil
}

// Active reports whether first-run setup mode is currently armed.
func (m *SetupManager) Active() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// VerifySecret compares candidate against the stored SHA-256 setup secret digest
// in constant time.
func (m *SetupManager) VerifySecret(candidate string) bool {
	if m == nil || candidate == "" {
		return false
	}

	m.mu.RLock()
	active := m.active
	expected := m.digest
	m.mu.RUnlock()

	if !active {
		return false
	}

	actual := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}

// Clear disables first-run setup mode and zeroes the stored SHA-256 digest.
func (m *SetupManager) Clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.active = false
	for i := range m.digest {
		m.digest[i] = 0
	}
	m.mu.Unlock()
}

// GenerateSetupSecret returns a random 128-bit single-use secret formatted as
// bbx-setup-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx-xxxx.
func GenerateSetupSecret() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate setup secret: %w", err)
	}
	hexStr := hex.EncodeToString(raw)
	groups := make([]string, 0, 8)
	for i := 0; i < len(hexStr); i += 4 {
		groups = append(groups, hexStr[i:i+4])
	}
	return "bbx-setup-" + strings.Join(groups, "-"), nil
}
