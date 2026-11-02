package codexplan

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
)

var errIdentity = errors.New("invalid ChatGPT identity")

func (o *OAuth) identity(ctx context.Context, token, clientID, nonce string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errIdentity
	}
	var header struct {
		Algorithm string `json:"alg"`
		Key       string `json:"kid"`
	}
	var claims struct {
		Issuer   string          `json:"iss"`
		Subject  string          `json:"sub"`
		Audience json.RawMessage `json:"aud"`
		Party    string          `json:"azp"`
		Nonce    string          `json:"nonce"`
		Expires  int64           `json:"exp"`
		Issued   int64           `json:"iat"`
	}
	if !decodeJWTPart(parts[0], &header) || header.Algorithm != "RS256" || header.Key == "" ||
		!decodeJWTPart(parts[1], &claims) || claims.Issuer != issuer || claims.Subject == "" ||
		len(claims.Subject) > 255 || claims.Nonce != nonce {
		return "", errIdentity
	}
	var audience []string
	if err := json.Unmarshal(claims.Audience, &audience); err != nil {
		var one string
		if json.Unmarshal(claims.Audience, &one) != nil {
			return "", errIdentity
		}
		audience = []string{one}
	}
	now := o.now().Unix()
	if claims.Expires <= now || claims.Issued <= 0 || claims.Issued > now+60 ||
		!slices.Contains(audience, clientID) || ((len(audience) > 1 || claims.Party != "") && claims.Party != clientID) {
		return "", errIdentity
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errIdentity
	}
	key, err := o.signingKey(ctx, header.Key)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) != nil {
		return "", errIdentity
	}
	return claims.Subject, nil
}

func decodeJWTPart(part string, dest any) bool {
	data, err := base64.RawURLEncoding.DecodeString(part)
	return err == nil && json.Unmarshal(data, dest) == nil
}

func (o *OAuth) signingKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, errIdentity
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errIdentity
	}
	defer func() { _ = resp.Body.Close() }()
	var jwks struct {
		Keys []struct {
			ID        string `json:"kid"`
			Type      string `json:"kty"`
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
			N         string `json:"n"`
			E         string `json:"e"`
		} `json:"keys"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&jwks) != nil {
		return nil, errIdentity
	}
	for _, key := range jwks.Keys {
		if key.ID != keyID || key.Type != "RSA" || (key.Algorithm != "" && key.Algorithm != "RS256") || (key.Use != "" && key.Use != "sig") {
			continue
		}
		n, nErr := base64.RawURLEncoding.DecodeString(key.N)
		e, eErr := base64.RawURLEncoding.DecodeString(key.E)
		if nErr != nil || eErr != nil || len(e) > 4 {
			return nil, errIdentity
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := new(big.Int).SetBytes(e).Int64()
		if modulus.BitLen() < 2048 || exponent < 3 || exponent > 1<<31-1 || exponent%2 == 0 {
			return nil, errIdentity
		}
		return &rsa.PublicKey{N: modulus, E: int(exponent)}, nil
	}
	return nil, errIdentity
}
