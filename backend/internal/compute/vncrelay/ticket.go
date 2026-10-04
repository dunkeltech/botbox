// Package vncrelay implements short-lived HMAC-signed VNC stream tickets and an
// authenticated WebSocket relay with server-side viewer input enforcement.
package vncrelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const minSecretBytes = 16

var (
	// ErrInvalidTicket indicates a malformed, empty, or signature-failed ticket.
	ErrInvalidTicket = errors.New("invalid VNC ticket")
	// ErrExpiredTicket indicates a ticket whose expiry timestamp has passed.
	ErrExpiredTicket = errors.New("expired VNC ticket")
	// ErrCrossWorkspace indicates a ticket scoped to a different workspace_id.
	// Callers must translate this to 404 Not Found to preserve tenant isolation.
	ErrCrossWorkspace = errors.New("ticket belongs to another workspace")
)

// TicketClaims binds a VNC WebSocket session to a workspace, bot, and viewer role.
type TicketClaims struct {
	WorkspaceID string    `json:"workspace_id"`
	BotID       string    `json:"bot_id"`
	UserID      string    `json:"user_id"`
	CanControl  bool      `json:"can_control"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Signer signs and verifies short-lived VNC WebSocket tickets using HMAC-SHA256.
type Signer struct {
	secret []byte
}

// NewSigner creates a Signer using a symmetric secret of at least 16 bytes.
func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) < minSecretBytes {
		return nil, fmt.Errorf("VNC ticket secret must be at least %d bytes", minSecretBytes)
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &Signer{secret: copied}, nil
}

// Sign serializes and signs TicketClaims into an opaque URL-safe token.
func (s *Signer) Sign(claims TicketClaims) (string, error) {
	if s == nil {
		return "", errors.New("nil VNC ticket signer")
	}
	if claims.WorkspaceID == "" || claims.BotID == "" || claims.UserID == "" {
		return "", fmt.Errorf("%w: missing required claims", ErrInvalidTicket)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal ticket claims: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := s.computeMAC(encodedPayload)
	encodedSig := base64.RawURLEncoding.EncodeToString(sig)
	return encodedPayload + "." + encodedSig, nil
}

// Verify validates signature, expiry, bot binding, and workspace isolation.
func (s *Signer) Verify(token, expectedWorkspaceID, expectedBotID string, now time.Time) (*TicketClaims, error) {
	if s == nil {
		return nil, ErrInvalidTicket
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, ErrInvalidTicket
	}

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidTicket
	}
	wantSig := s.computeMAC(parts[0])
	if !hmac.Equal(gotSig, wantSig) {
		return nil, ErrInvalidTicket
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidTicket
	}

	var claims TicketClaims
	if err := json.Unmarshal(rawPayload, &claims); err != nil {
		return nil, ErrInvalidTicket
	}

	if claims.WorkspaceID == "" || claims.BotID == "" || claims.UserID == "" {
		return nil, ErrInvalidTicket
	}

	// Enforce tenant isolation first so cross-workspace guesses map to 404 Not Found.
	if claims.WorkspaceID != expectedWorkspaceID {
		return nil, ErrCrossWorkspace
	}
	if claims.BotID != expectedBotID {
		return nil, ErrInvalidTicket
	}
	if !now.Before(claims.ExpiresAt) {
		return nil, ErrExpiredTicket
	}

	return &claims, nil
}

func (s *Signer) computeMAC(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}
