// SPDX-License-Identifier: AGPL-3.0-only

package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type fixtureResolver struct {
	addresses []netip.Addr
	err       error
}

func (r fixtureResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

func TestDialPublicUsesValidatedAddressAndRejectsUnsafeDNS(t *testing.T) {
	for _, tt := range []struct {
		name      string
		addresses []string
		valid     bool
	}{
		{"public", []string{"8.8.8.8"}, true},
		{"private 10/8", []string{"10.0.0.1"}, false},
		{"private 172.16/12 docker bridge", []string{"172.17.0.1"}, false},
		{"private 192.168/16", []string{"192.168.1.1"}, false},
		{"loopback ipv4", []string{"127.0.0.1"}, false},
		{"loopback ipv6", []string{"::1"}, false},
		{"ipv4 mapped loopback", []string{"::ffff:127.0.0.1"}, false},
		{"aws metadata", []string{"169.254.169.254"}, false},
		{"azure wireserver", []string{"168.63.129.16"}, false},
		{"ipv6 unique local metadata", []string{"fd00:ec2::254"}, false},
		{"mixed", []string{"8.8.8.8", "169.254.169.254"}, false},
		{"NAT64", []string{"64:ff9b::7f00:1"}, false},
		{"site local", []string{"fec0::1"}, false},
		{"documentation IPv4", []string{"192.0.2.1"}, false},
		{"documentation IPv6", []string{"2001:db8::1"}, false},
		{"reserved IPv4", []string{"240.0.0.1"}, false},
		{"empty", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := fixtureResolver{}
			for _, address := range tt.addresses {
				resolver.addresses = append(resolver.addresses, netip.MustParseAddr(address))
			}
			called := false
			dial := func(_ context.Context, network, addr string) (net.Conn, error) {
				called = true
				if network != "tcp" || addr != "8.8.8.8:443" {
					t.Fatalf("unvalidated dial: %s %s", network, addr)
				}
				a, b := net.Pipe()
				_ = b.Close()
				return a, nil
			}
			conn, err := DialPublic(context.Background(), resolver, dial, "tcp", "gitlab.example.org:443")
			if conn != nil {
				_ = conn.Close()
			}
			if (err == nil) != tt.valid || called != tt.valid {
				t.Fatalf("valid=%v called=%v err=%v", tt.valid, called, err)
			}
		})
	}
}

func TestDialPublicRejectsResolutionAndDialFailures(t *testing.T) {
	failure := errors.New("fixture unavailable")
	if _, err := DialPublic(context.Background(), fixtureResolver{err: failure}, nil, "tcp", "gitlab.example.org:443"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("DNS failure: %v", err)
	}
	if _, err := DialPublic(context.Background(), fixtureResolver{}, nil, "tcp", "gitlab.example.org:8443"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("wrong port: %v", err)
	}
	_, err := DialPublic(context.Background(), fixtureResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, func(context.Context, string, string) (net.Conn, error) { return nil, failure }, "tcp", "gitlab.example.org:443")
	if !errors.Is(err, failure) {
		t.Fatalf("dial failure: %v", err)
	}
}

type sequenceResolver struct {
	answers [][]netip.Addr
	calls   int
}

func (s *sequenceResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	idx := s.calls
	s.calls++
	if idx >= len(s.answers) {
		idx = len(s.answers) - 1
	}
	return s.answers[idx], nil
}

func TestDialPublicDefeatsDNSRebinding(t *testing.T) {
	resolver := &sequenceResolver{
		answers: [][]netip.Addr{
			{netip.MustParseAddr("93.184.216.34")},
			{netip.MustParseAddr("169.254.169.254")},
		},
	}
	var dialed []string
	conn, err := DialPublic(context.Background(), resolver, func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}, "tcp", "rebind.example.com:443")
	if err != nil {
		t.Fatalf("first DialPublic unexpected err: %v", err)
	}
	_ = conn.Close()
	if resolver.calls != 1 || len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("expected single lookup and literal public dial, calls=%d dialed=%v", resolver.calls, dialed)
	}
	if _, err := DialPublic(context.Background(), resolver, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("rebinding answer must never be dialed")
		return nil, nil
	}, "tcp", "rebind.example.com:443"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("expected ErrUnsafeDestination on rebound private answer, got %v", err)
	}
}
