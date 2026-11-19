// SPDX-License-Identifier: AGPL-3.0-only

// Package mitmproxy implements per-bot selective TLS interception for egress secrets.
package mitmproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets"
)

const (
	defaultCATTL   = 24 * time.Hour
	defaultLeafTTL = 12 * time.Hour
)

// BotCA holds a short-lived, per-bot ECDSA P-256 root CA used solely inside one sandbox's egress proxy.
type BotCA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey

	mu    sync.RWMutex
	cache map[string]*tls.Certificate
}

// NewBotCA mints an isolated ECDSA P-256 CA certificate for botID.
// Each bot gets its own CA key pair so compromise or leak of one sandbox never crosses bots.
func NewBotCA(botID string) (*BotCA, error) {
	return newBotCA(botID, defaultCATTL)
}

func newBotCA(botID string, ttl time.Duration) (*BotCA, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, errors.New("bot ID is required for CA generation")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate bot CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "botbox-bot-ca-" + botID,
			Organization: []string{"bot.Box Ephemeral Bot CA"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create bot CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse bot CA certificate: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &BotCA{
		cert:    cert,
		certPEM: pemBytes,
		key:     key,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

// LoadWorkerCA persists the worker's signing key independently of the relay
// instance. Only the public certificate is published in the desktop's volume.
func LoadWorkerCA(ring *secrets.KeyRing, workspace, botID, privateDir, publicDir string) (*BotCA, error) {
	if ring == nil || workspace == "" || botID == "" {
		return nil, errors.New("worker CA identity or key ring unavailable")
	}
	const keyFile = "ca.envelope"
	keyPath := filepath.Join(privateDir, keyFile)
	aad := []byte("botbox-egress-ca:" + workspace + ":" + botID)
	var ca *BotCA
	encoded, err := os.ReadFile(keyPath) //nolint:gosec // dedicated worker-owned CA volume
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A stable root avoids breaking desktop TLS when only the relay or
		// worker is replaced. The desktop refreshes its trust bundle on restart.
		ca, err = newBotCA(botID, 365*24*time.Hour)
		if err != nil {
			return nil, err
		}
		if err := storeWorkerCA(ca, ring, aad, privateDir, keyFile); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		ca, err = restoreWorkerCA(encoded, ring, aad, privateDir, keyFile)
		if err != nil {
			return nil, err
		}
	}
	if ca.cert.Subject.CommonName != "botbox-bot-ca-"+botID {
		return nil, errors.New("worker CA belongs to a different bot")
	}
	if err := publishCAFile(publicDir, "ca.crt", ca.CertPEM(), 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

func storeWorkerCA(ca *BotCA, ring *secrets.KeyRing, aad []byte, dir, name string) error {
	key, err := x509.MarshalECPrivateKey(ca.key)
	if err != nil {
		return err
	}
	defer clear(key)
	bundle := append(ca.CertPEM(), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key})...)
	defer clear(bundle)
	envelope, err := ring.Encrypt(bundle, aad)
	if err != nil {
		return err
	}
	return publishCAEnvelope(dir, name, envelope)
}

func restoreWorkerCA(encoded []byte, ring *secrets.KeyRing, aad []byte, dir, name string) (*BotCA, error) {
	var envelope secrets.EnvelopeCiphertext
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, errors.New("invalid worker CA envelope")
	}
	bundle, err := ring.Decrypt(envelope, aad)
	if err != nil {
		return nil, err
	}
	defer clear(bundle)
	cert, key := pem.Decode(bundle)
	if cert == nil || cert.Type != "CERTIFICATE" {
		return nil, errors.New("invalid worker CA certificate")
	}
	certPEM := pem.EncodeToMemory(cert)
	pair, err := tls.X509KeyPair(certPEM, key)
	if err != nil {
		return nil, errors.New("invalid worker CA key pair")
	}
	privateKey, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || pair.Leaf == nil || !pair.Leaf.IsCA || !time.Now().Before(pair.Leaf.NotAfter) {
		return nil, errors.New("worker CA unavailable or expired")
	}
	if envelope.KeyID != ring.PrimaryID() {
		rewrapped, err := ring.RewrapDataKey(envelope, aad)
		if err != nil {
			return nil, err
		}
		if err := publishCAEnvelope(dir, name, rewrapped); err != nil {
			return nil, err
		}
	}
	return &BotCA{cert: pair.Leaf, certPEM: certPEM, key: privateKey, cache: make(map[string]*tls.Certificate)}, nil
}

func publishCAEnvelope(dir, name string, envelope secrets.EnvelopeCiphertext) error {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return publishCAFile(dir, name, encoded, 0o600)
}

func publishCAFile(dir, name string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(dir, ".ca-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name()) //nolint:errcheck // best-effort cleanup after rename or error
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

// CertPEM returns only the public CA certificate PEM to mount read-only into the bot sandbox.
func (ca *BotCA) CertPEM() []byte {
	out := make([]byte, len(ca.certPEM))
	copy(out, ca.certPEM)
	return out
}

// CertPool returns an x509.CertPool trusting solely this bot's CA certificate.
func (ca *BotCA) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// LeafCertificate returns a cached or newly signed ECDSA P-256 leaf certificate for host.
func (ca *BotCA) LeafCertificate(host string) (*tls.Certificate, error) {
	cleanHost := normalizeHost(host)
	if cleanHost == "" {
		return nil, errors.New("leaf host is required")
	}

	now := time.Now().UTC()
	ca.mu.RLock()
	if cached, ok := ca.cache[cleanHost]; ok && cached.Leaf != nil && now.Add(5*time.Minute).Before(cached.Leaf.NotAfter) {
		ca.mu.RUnlock()
		return cached, nil
	}
	ca.mu.RUnlock()

	ca.mu.Lock()
	defer ca.mu.Unlock()
	if cached, ok := ca.cache[cleanHost]; ok && cached.Leaf != nil && now.Add(5*time.Minute).Before(cached.Leaf.NotAfter) {
		return cached, nil
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key for %s: %w", cleanHost, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: cleanHost,
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(defaultLeafTTL),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(cleanHost); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{cleanHost}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf certificate for %s: %w", cleanHost, err)
	}
	parsedLeaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate for %s: %w", cleanHost, err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{der, ca.cert.Raw},
		PrivateKey:  leafKey,
		Leaf:        parsedLeaf,
	}
	ca.cache[cleanHost] = tlsCert
	return tlsCert, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

func normalizeHost(hostPort string) string {
	trimmed := strings.TrimSpace(hostPort)
	if trimmed == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	trimmed = strings.Trim(trimmed, "[]")
	return strings.ToLower(trimmed)
}
