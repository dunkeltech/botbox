// Package netguard validates public DNS answers and dials their literal addresses.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// ErrUnsafeDestination indicates DNS did not resolve exclusively to permitted public addresses.
var ErrUnsafeDestination = errors.New("destination is not a permitted public host")

// Resolver provides context-bound DNS lookups for actual outbound connections.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// ResolvePublicAddresses rejects empty, mixed, private and infrastructure DNS answers.
func ResolvePublicAddresses(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrUnsafeDestination
	}
	// Reject mixed answers rather than selecting a public answer from a private service.
	for _, address := range addresses {
		ip := address.Unmap()
		if !ip.IsValid() || isInfrastructureAddress(ip) || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, ErrUnsafeDestination
		}
	}
	return addresses, nil
}

func isInfrastructureAddress(ip netip.Addr) bool {
	// Unallocated IPv6 space and special-purpose ranges are never valid public GitLab targets.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return true
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "168.63.129.16/32", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return true
		}
	}
	return false
}

// DialPublic resolves once, rejects unsafe/mixed answers, and dials a validated IP without re-resolving.
func DialPublic(ctx context.Context, resolver Resolver, dial func(context.Context, string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port != "443" {
		return nil, ErrUnsafeDestination
	}
	addresses, err := ResolvePublicAddresses(ctx, resolver, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		conn, dialErr := dial(ctx, network, net.JoinHostPort(address.Unmap().String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	return nil, err
}
