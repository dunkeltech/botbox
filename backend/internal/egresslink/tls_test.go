// SPDX-License-Identifier: AGPL-3.0-only

package egresslink

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"
)

func TestTransportBindsBothPeers(t *testing.T) {
	gateway, sidecar, err := NewPair()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"paired sidecar", true},
		{"different sidecar", false},
		{"different gateway", false},
		{"expired instance certificate", false},
		{"missing instance certificate", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverPeer := gateway.seed.Public().(ed25519.PublicKey)
			clientSeed := sidecar.seed
			if tc.name == "different sidecar" {
				_, clientSeed, err = ed25519.GenerateKey(rand.Reader)
			}
			if tc.name == "different gateway" {
				serverPeer, _, err = ed25519.GenerateKey(rand.Reader)
			}
			if err != nil {
				t.Fatal(err)
			}
			server := gateway.TLS(true)
			instance := Identity{seed: clientSeed, Peer: serverPeer}
			client := instance.TLS(false)
			switch tc.name {
			case "expired instance certificate":
				client.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					return instance.issue(x509.ExtKeyUsageClientAuth, time.Now().Add(-time.Hour))
				}
			case "missing instance certificate":
				client.GetClientCertificate = nil
			}
			serverRaw, clientRaw := net.Pipe()
			defer func() { _ = serverRaw.Close() }()
			defer func() { _ = clientRaw.Close() }()
			serverConn := tls.Server(serverRaw, server)
			clientConn := tls.Client(clientRaw, client)
			done := make(chan error, 1)
			go func() {
				serverErr := serverConn.HandshakeContext(t.Context())
				if serverErr == nil {
					_, serverErr = serverConn.Write([]byte("x"))
				}
				_ = serverRaw.Close()
				done <- serverErr
			}()
			_, clientErr := clientConn.Read(make([]byte, 1))
			_ = clientRaw.Close()
			if serverErr := <-done; (clientErr == nil && serverErr == nil) != tc.ok {
				t.Fatalf("client error = %v, server error = %v", clientErr, serverErr)
			}
		})
	}
}
