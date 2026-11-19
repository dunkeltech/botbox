// SPDX-License-Identifier: AGPL-3.0-only

// Package egresslink authenticates the private transport between a bot's
// credential-free relay and its trusted egress worker.
package egresslink

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math/big"
	"time"
)

const certificateTTL = 5 * time.Minute

// Identity binds a transport endpoint to exactly one provisioned peer key.
type Identity struct {
	seed ed25519.PrivateKey
	Peer ed25519.PublicKey
}

// NewPair generates independent credentials for one worker and its relay.
func NewPair() (worker, relay Identity, err error) {
	workerPublic, workerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, Identity{}, err
	}
	relayPublic, relayKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, Identity{}, err
	}
	return Identity{seed: workerKey, Peer: relayPublic}, Identity{seed: relayKey, Peer: workerPublic}, nil
}

// Environment returns the local instance key and the peer's public key.
func (i Identity) Environment() []string {
	return []string{
		"EGRESS_LINK_SEED=" + base64.RawStdEncoding.EncodeToString(i.seed.Seed()),
		"EGRESS_LINK_PEER=" + base64.RawStdEncoding.EncodeToString(i.Peer),
	}
}

// ParseIdentity reconstructs a provisioned transport endpoint.
func ParseIdentity(seedValue, peerValue string) (Identity, error) {
	seed, err := base64.RawStdEncoding.DecodeString(seedValue)
	if err != nil || len(seed) != ed25519.SeedSize {
		return Identity{}, errors.New("invalid egress instance key")
	}
	peer, err := base64.RawStdEncoding.DecodeString(peerValue)
	if err != nil || len(peer) != ed25519.PublicKeySize {
		return Identity{}, errors.New("invalid egress peer key")
	}
	return Identity{seed: ed25519.NewKeyFromSeed(seed), Peer: ed25519.PublicKey(peer)}, nil
}

// TLS issues short-lived certificates for the instance and authenticates its
// pinned peer. Resumption would bypass proof of possession on a new socket.
func (i Identity) TLS(server bool) *tls.Config {
	localUsage, peerUsage := x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth
	if server {
		localUsage, peerUsage = peerUsage, localUsage
	}
	config := &tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("missing egress peer certificate")
			}
			cert := state.PeerCertificates[0]
			if !i.Peer.Equal(cert.PublicKey) || cert.NotAfter.Sub(cert.NotBefore) > certificateTTL+time.Minute {
				return errors.New("unrecognized egress peer")
			}
			roots := x509.NewCertPool()
			roots.AddCert(cert)
			_, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{peerUsage}})
			return err
		},
	}
	if server {
		config.ClientAuth = tls.RequireAnyClientCert
		config.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return i.issue(localUsage, time.Now())
		}
	} else {
		// The worker key was provisioned directly. VerifyConnection enforces the
		// pin and certificate lifetime instead of trusting public CA roots.
		config.InsecureSkipVerify = true //nolint:gosec // VerifyConnection authenticates the configured peer key
		config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return i.issue(localUsage, time.Now())
		}
	}
	return config
}

func (i Identity) issue(usage x509.ExtKeyUsage, now time.Time) (*tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(certificateTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, i.seed.Public(), i.seed)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: i.seed}, nil
}
