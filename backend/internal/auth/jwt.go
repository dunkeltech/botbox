package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultAccessTokenTTL is the default lifetime for EdDSA JWT access tokens.
	DefaultAccessTokenTTL = 15 * time.Minute
	// DefaultRefreshTokenTTL is the default lifetime for opaque refresh tokens.
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour

	jwtIssuer        = "botbox"
	maxClockSkewSecs = 30
)

var (
	// ErrInvalidCredentials indicates unknown email or wrong password.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidToken indicates an invalid, tampered, or unsupported JWT access token.
	ErrInvalidToken = errors.New("invalid access token")
	// ErrExpiredToken indicates an expired JWT access token.
	ErrExpiredToken = errors.New("expired access token")
)

// Credentials represents local email/password login inputs passed to AuthProvider.
type Credentials struct {
	Email    string
	Password string
}

// User represents an authenticated user principal without any secret fields.
type User struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	Locale      string
	CreatedAt   time.Time
}

// StoredUser represents the database user record returned by LocalUserStore.
type StoredUser struct {
	User
	PasswordHash string
}

// Claims holds validated identity claims from a JWT access token.
type Claims struct {
	Subject     uuid.UUID
	Email       string
	DisplayName string
	JWTID       string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

// Provider defines the modular AuthProvider contract from Application.md §4.1.
type Provider interface {
	Authenticate(ctx context.Context, credentials Credentials) (*User, error)
	ValidateToken(ctx context.Context, token string) (*Claims, error)
}

// LocalUserStore abstracts database lookups and hash upgrades for LocalProvider.
type LocalUserStore interface {
	GetUserByEmail(ctx context.Context, email string) (*StoredUser, error)
	UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error
}

// JWTService issues and verifies EdDSA (Ed25519) access tokens.
type JWTService struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	ttl  time.Duration
	now  func() time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtPayload struct {
	Iss         string `json:"iss"`
	Sub         string `json:"sub"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Jti         string `json:"jti"`
	Iat         int64  `json:"iat"`
	Exp         int64  `json:"exp"`
}

// GenerateEd25519Key creates a fresh Ed25519 signing key pair.
func GenerateEd25519Key() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return priv, nil
}

// ParseEd25519Seed decodes a base64-encoded 32-byte Ed25519 seed into a private key.
func ParseEd25519Seed(encodedSeed string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedSeed))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("invalid base64 ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// NewJWTService constructs an EdDSA JWT signer and verifier.
func NewJWTService(privKey ed25519.PrivateKey, ttl time.Duration, now func() time.Time) (*JWTService, error) {
	if len(privKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid ed25519 private key length")
	}
	if ttl <= 0 {
		ttl = DefaultAccessTokenTTL
	}
	if now == nil {
		now = time.Now
	}
	pub, ok := privKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("extract ed25519 public key")
	}
	return &JWTService{
		priv: privKey,
		pub:  pub,
		ttl:  ttl,
		now:  now,
	}, nil
}

// IssueAccessToken creates a signed EdDSA JWT for user.
func (s *JWTService) IssueAccessToken(user User) (string, error) {
	issuedAt := s.now().UTC()
	expiresAt := issuedAt.Add(s.ttl)

	hdrBytes, err := json.Marshal(jwtHeader{Alg: "EdDSA", Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	payBytes, err := json.Marshal(jwtPayload{
		Iss:         jwtIssuer,
		Sub:         user.ID.String(),
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Jti:         uuid.NewString(),
		Iat:         issuedAt.Unix(),
		Exp:         expiresAt.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal jwt payload: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	sig := ed25519.Sign(s.priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ValidateToken verifies the signature, header algorithm, issuer, subject UUID, and expiration.
func (s *JWTService) ValidateToken(_ context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil || hdr.Alg != "EdDSA" || hdr.Typ != "JWT" {
		return nil, ErrInvalidToken
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return nil, ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(s.pub, []byte(signingInput), sigBytes) {
		return nil, ErrInvalidToken
	}

	payBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var pay jwtPayload
	if err := json.Unmarshal(payBytes, &pay); err != nil || pay.Iss != jwtIssuer {
		return nil, ErrInvalidToken
	}

	sub, err := uuid.Parse(pay.Sub)
	if err != nil {
		return nil, ErrInvalidToken
	}

	now := s.now().UTC().Unix()
	if pay.Exp <= now {
		return nil, ErrExpiredToken
	}
	if pay.Iat > now+maxClockSkewSecs {
		return nil, ErrInvalidToken
	}

	return &Claims{
		Subject:     sub,
		Email:       pay.Email,
		DisplayName: pay.DisplayName,
		JWTID:       pay.Jti,
		IssuedAt:    time.Unix(pay.Iat, 0).UTC(),
		ExpiresAt:   time.Unix(pay.Exp, 0).UTC(),
	}, nil
}

// LocalProvider implements Provider backed by PostgreSQL users and Argon2id hashes.
type LocalProvider struct {
	store  LocalUserStore
	jwt    *JWTService
	params Argon2Params
	// dummyHash equalizes timing when an email is unknown.
	dummyHash string
}

// NewLocalProvider constructs a local Argon2id + EdDSA JWT AuthProvider.
func NewLocalProvider(store LocalUserStore, jwt *JWTService, params Argon2Params) (*LocalProvider, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	dummy, err := HashPassword("dummy-password-for-timing", params)
	if err != nil {
		return nil, fmt.Errorf("create dummy hash: %w", err)
	}
	return &LocalProvider{
		store:     store,
		jwt:       jwt,
		params:    params,
		dummyHash: dummy,
	}, nil
}

// NormalizeEmail canonicalizes email addresses before lookup or storage.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Authenticate verifies local email/password credentials and transparently upgrades
// Argon2id hashes when the configured target parameters change.
func (p *LocalProvider) Authenticate(ctx context.Context, credentials Credentials) (*User, error) {
	email := NormalizeEmail(credentials.Email)
	if email == "" || credentials.Password == "" {
		return nil, ErrInvalidCredentials
	}

	stored, err := p.store.GetUserByEmail(ctx, email)
	if err != nil {
		// Burn the same Argon2id work factor even when the account is missing.
		_, _, _ = VerifyPassword(credentials.Password, p.dummyHash, p.params)
		return nil, ErrInvalidCredentials
	}

	match, needsUpgrade, verifyErr := VerifyPassword(credentials.Password, stored.PasswordHash, p.params)
	if verifyErr != nil || !match {
		return nil, ErrInvalidCredentials
	}

	if needsUpgrade {
		// Validate length before re-hashing; if legacy password exceeds limits, skip silently.
		if upgraded, hashErr := HashPassword(credentials.Password, p.params); hashErr == nil {
			_ = p.store.UpdatePasswordHash(ctx, stored.ID, upgraded)
		}
	}

	user := stored.User
	return &user, nil
}

// ValidateToken validates an EdDSA JWT access token.
func (p *LocalProvider) ValidateToken(ctx context.Context, token string) (*Claims, error) {
	return p.jwt.ValidateToken(ctx, token)
}
