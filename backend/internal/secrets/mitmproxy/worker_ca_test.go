// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets"
)

func TestWorkerCAPublishesOnlyStablePublicCertificate(t *testing.T) {
	privateDir, publicDir := t.TempDir(), t.TempDir()
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	ring, err := secrets.NewKeyRing("old", map[string][]byte{"old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadWorkerCA(ring, "workspace", "bot-one", privateDir, publicDir)
	if err != nil {
		t.Fatal(err)
	}
	rotating, err := secrets.NewKeyRing("new", map[string][]byte{"old": oldKey, "new": newKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = LoadWorkerCA(rotating, "workspace", "bot-one", privateDir, publicDir); err != nil {
		t.Fatal(err)
	}
	rotated, err := secrets.NewKeyRing("new", map[string][]byte{"new": newKey})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadWorkerCA(rotated, "workspace", "bot-one", privateDir, publicDir)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join(publicDir, "ca.crt")) //nolint:gosec // test-owned temporary volume
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(public)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 ||
		!bytes.Equal(first.CertPEM(), public) || !bytes.Equal(restarted.CertPEM(), public) {
		t.Fatal("desktop volume does not contain exactly the stable public CA")
	}
	leaf, err := restarted.LeafCertificate("fixture.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{Roots: first.CertPool(), DNSName: "fixture.example"}); err != nil {
		t.Fatal("worker restart broke desktop trust:", err)
	}
	if _, err := LoadWorkerCA(rotated, "workspace", "bot-two", privateDir, publicDir); err == nil {
		t.Fatal("worker accepted another bot's signing key")
	}
}
