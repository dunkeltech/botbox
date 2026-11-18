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
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
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
		NotAfter:              now.Add(defaultCATTL),
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
