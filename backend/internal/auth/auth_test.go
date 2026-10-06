package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fastTestArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKiB:   4096,
		Iterations:  1,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}
}

func TestPasswordHashVerifyAndUpgrade(t *testing.T) {
	t.Parallel()

	oldParams := fastTestArgon2Params()
	newParams := Argon2Params{
		MemoryKiB:   8192,
		Iterations:  2,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}

	hash, err := HashPassword("correct-horse-battery", oldParams)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=4096,t=1,p=1$") {
		t.Fatalf("unexpected PHC hash prefix: %s", hash)
	}

	match, upgrade, err := VerifyPassword("correct-horse-battery", hash, oldParams)
	if err != nil || !match || upgrade {
		t.Fatalf("expected match=true, upgrade=false, err=nil; got match=%v upgrade=%v err=%v", match, upgrade, err)
	}

	// Parameter upgrade requested when target parameters change.
	match, upgrade, err = VerifyPassword("correct-horse-battery", hash, newParams)
	if err != nil || !match || !upgrade {
		t.Fatalf("expected match=true, upgrade=true, err=nil; got match=%v upgrade=%v err=%v", match, upgrade, err)
	}

	// Wrong password never signals upgrade.
	match, upgrade, err = VerifyPassword("wrong-password-attempt", hash, newParams)
	if err != nil || match || upgrade {
		t.Fatalf("expected match=false, upgrade=false, err=nil; got match=%v upgrade=%v err=%v", match, upgrade, err)
	}
}

func TestPasswordNegativeCases(t *testing.T) {
	t.Parallel()

	params := fastTestArgon2Params()
	for _, pw := range []string{"short", "11-char-pw!", strings.Repeat("x", 129)} {
		if _, err := HashPassword(pw, params); !errors.Is(err, ErrInvalidPasswordLength) {
			t.Fatalf("HashPassword(%d bytes) err = %v, want %v", len(pw), err, ErrInvalidPasswordLength)
		}
	}

	badParams := Argon2Params{MemoryKiB: 512, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	if _, err := HashPassword("valid-long-password", badParams); !errors.Is(err, ErrInvalidArgon2Params) {
		t.Fatalf("HashPassword with weak memory err = %v, want %v", err, ErrInvalidArgon2Params)
	}

	for _, badHash := range []string{
		"",
		"$bcrypt$v=19$m=4096,t=1,p=1$abcd$abcd",
		"$argon2id$v=99$m=4096,t=1,p=1$abcd$abcd",
		"$argon2id$v=19$m=4096,t=1,p=999$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=4096,t=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=4096,t=1,p=1$!!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if _, _, err := VerifyPassword("valid-long-password", badHash, params); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("VerifyPassword(%q) err = %v, want %v", badHash, err, ErrInvalidPasswordHash)
		}
	}
}

type memoryUserStore struct {
	user StoredUser
}

func (m *memoryUserStore) GetUserByEmail(_ context.Context, email string) (*StoredUser, error) {
	if email != m.user.Email {
		return nil, ErrInvalidCredentials
	}
	cp := m.user
	return &cp, nil
}

func (m *memoryUserStore) UpdatePasswordHash(_ context.Context, userID uuid.UUID, passwordHash string) error {
	if userID != m.user.ID {
		return ErrInvalidCredentials
	}
	m.user.PasswordHash = passwordHash
	return nil
}

func TestLocalProviderAuthenticateUpgradesHash(t *testing.T) {
	t.Parallel()

	oldParams := fastTestArgon2Params()
	targetParams := Argon2Params{
		MemoryKiB:   8192,
		Iterations:  2,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}

	oldHash, err := HashPassword("alpine-cedar-forge", oldParams)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	store := &memoryUserStore{
		user: StoredUser{
			User: User{
				ID:          uuid.New(),
				Email:       "jan@dunkel.tech",
				DisplayName: "Jan",
				Locale:      "en",
			},
			PasswordHash: oldHash,
		},
	}

	key, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	jwtSvc, err := NewJWTService(key, DefaultAccessTokenTTL, time.Now)
	if err != nil {
		t.Fatalf("NewJWTService failed: %v", err)
	}

	provider, err := NewLocalProvider(store, jwtSvc, targetParams)
	if err != nil {
		t.Fatalf("NewLocalProvider failed: %v", err)
	}

	// Wrong password fails and preserves original hash.
	if _, err := provider.Authenticate(context.Background(), Credentials{
		Email:    "Jan@Dunkel.Tech ",
		Password: "wrong-password-123",
	}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v, want %v", err, ErrInvalidCredentials)
	}
	if store.user.PasswordHash != oldHash {
		t.Fatal("password hash changed after failed login")
	}

	// Unknown email also fails.
	if _, err := provider.Authenticate(context.Background(), Credentials{
		Email:    "missing@dunkel.tech",
		Password: "alpine-cedar-forge",
	}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v, want %v", err, ErrInvalidCredentials)
	}

	// Case-insensitive valid login succeeds and upgrades hash.
	user, err := provider.Authenticate(context.Background(), Credentials{
		Email:    " JAN@DUNKEL.TECH ",
		Password: "alpine-cedar-forge",
	})
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if user.ID != store.user.ID || user.Email != "jan@dunkel.tech" {
		t.Fatalf("unexpected user: %+v", user)
	}
	if !strings.HasPrefix(store.user.PasswordHash, "$argon2id$v=19$m=8192,t=2,p=1$") {
		t.Fatalf("expected upgraded hash prefix, got %s", store.user.PasswordHash)
	}
}

func TestJWTIssueValidateAndExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	key, err := GenerateEd25519Key()
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	svc, err := NewJWTService(key, 15*time.Minute, clock)
	if err != nil {
		t.Fatalf("NewJWTService failed: %v", err)
	}

	u := User{
		ID:          uuid.New(),
		Email:       "jan@dunkel.tech",
		DisplayName: "Jan Gehring",
	}
	token, err := svc.IssueAccessToken(u)
	if err != nil {
		t.Fatalf("IssueAccessToken failed: %v", err)
	}

	claims, err := svc.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}
	if claims.Subject != u.ID || claims.Email != u.Email || claims.DisplayName != u.DisplayName {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	// Tampered payload rejected.
	parts := strings.Split(token, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"botbox","sub":"` + uuid.NewString() + `","exp":9999999999}`))
	if _, err := svc.ValidateToken(context.Background(), strings.Join(parts, ".")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token err = %v, want %v", err, ErrInvalidToken)
	}

	// Wrong signing key rejected.
	otherKey, _ := GenerateEd25519Key()
	otherSvc, _ := NewJWTService(otherKey, 15*time.Minute, clock)
	if _, err := otherSvc.ValidateToken(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong key err = %v, want %v", err, ErrInvalidToken)
	}

	// Alg=none rejected even if signed with random bytes.
	noneHdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	noneTok := noneHdr + "." + parts[1] + "." + parts[2]
	if _, err := svc.ValidateToken(context.Background(), noneTok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("alg=none token err = %v, want %v", err, ErrInvalidToken)
	}

	// Expired exactly at TTL boundary.
	now = now.Add(15 * time.Minute)
	if _, err := svc.ValidateToken(context.Background(), token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expired token err = %v, want %v", err, ErrExpiredToken)
	}

	// Round-trip ParseEd25519Seed.
	seedB64 := base64.StdEncoding.EncodeToString(key.Seed())
	parsedKey, err := ParseEd25519Seed(seedB64)
	if err != nil || !ed25519.PrivateKey.Equal(parsedKey, key) {
		t.Fatalf("ParseEd25519Seed mismatch: err=%v", err)
	}
}

func TestRateLimiterDeterministicWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(3, 15*time.Minute, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !limiter.Allow("ip:127.0.0.1") {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		now = now.Add(time.Minute)
	}

	// Fourth attempt within 15 minutes fails, while a distinct key succeeds.
	if limiter.Allow("ip:127.0.0.1") {
		t.Fatal("expected fourth attempt within window to be rejected")
	}
	if !limiter.Allow("ip:10.0.0.2") {
		t.Fatal("distinct rate limit key should not be blocked")
	}

	// Advancing past the oldest attempt's expiration frees one slot.
	now = time.Date(2026, 11, 15, 12, 15, 1, 0, time.UTC)
	if !limiter.Allow("ip:127.0.0.1") {
		t.Fatal("expected oldest slot to expire cleanly")
	}
	if limiter.Allow("ip:127.0.0.1") {
		t.Fatal("expected second slot still active")
	}
}

func TestSetupManagerLifecycle(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	mgr := NewSetupManager(&stdout)
	secret, active, err := mgr.EnsureSetupSecret(context.Background(), 0, "", logger)
	if err != nil || !active || !strings.HasPrefix(secret, "bbx-setup-") {
		t.Fatalf("EnsureSetupSecret failed: secret=%q active=%v err=%v", secret, active, err)
	}
	if !strings.Contains(stdout.String(), secret) {
		t.Fatal("expected auto-generated setup secret to be printed to stdout")
	}
	if strings.Contains(logBuf.String(), secret) {
		t.Fatal("auto-generated setup secret must not appear in structured logs")
	}
	if !mgr.VerifySecret(secret) {
		t.Fatal("expected valid secret verification")
	}
	if mgr.VerifySecret("bbx-setup-wrong") || mgr.VerifySecret("") {
		t.Fatal("expected wrong/empty setup secret to fail")
	}

	mgr.Clear()
	if mgr.Active() || mgr.VerifySecret(secret) {
		t.Fatal("expected cleared setup manager to reject secret")
	}

	// Explicit env secret is never printed to stdout or logs.
	stdout.Reset()
	logBuf.Reset()
	wantEnvSecret := strings.Join([]string{"bbx", "setup", "env", "override"}, "-")
	envSecret, active, err := mgr.EnsureSetupSecret(context.Background(), 0, wantEnvSecret, logger)
	if err != nil || !active || envSecret != wantEnvSecret {
		t.Fatalf("env EnsureSetupSecret failed: secret=%q active=%v err=%v", envSecret, active, err)
	}
	if strings.Contains(stdout.String(), envSecret) || strings.Contains(logBuf.String(), envSecret) {
		t.Fatal("configured env setup secret must not appear in stdout or logs")
	}

	// Non-zero database disarms setup mode.
	_, active, err = mgr.EnsureSetupSecret(context.Background(), 1, envSecret, logger)
	if err != nil || active || mgr.VerifySecret(envSecret) {
		t.Fatalf("expected setup inactive when users > 0")
	}
}

func TestOpaqueTokenGeneration(t *testing.T) {
	t.Parallel()

	p1, h1, err := GenerateOpaqueToken("bbx-inv-", 32)
	if err != nil {
		t.Fatalf("GenerateOpaqueToken failed: %v", err)
	}
	p2, h2, err := GenerateOpaqueToken("bbx-inv-", 32)
	if err != nil {
		t.Fatalf("GenerateOpaqueToken failed: %v", err)
	}
	if p1 == p2 || bytes.Equal(h1, h2) {
		t.Fatal("expected unique opaque tokens")
	}
	if len(h1) != 32 || !bytes.Equal(h1, HashToken(p1)) {
		t.Fatal("token hash mismatch")
	}
}
