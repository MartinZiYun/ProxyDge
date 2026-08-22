package gateway

import (
	"fmt"
	"net"
)

// UntrustedAction is what the gateway does when a non-trusted source
// sends a PROXY header.
type UntrustedAction int

const (
	// UntrustedReject closes the connection (default).
	UntrustedReject UntrustedAction = iota
	// UntrustedStrip consumes the header, re-normalizes as a direct
	// connection using the real TCP peer address.
	UntrustedStrip
)

func (a UntrustedAction) String() string {
	switch a {
	case UntrustedReject:
		return "reject"
	case UntrustedStrip:
		return "strip"
	}
	return "unknown"
}

// TrustChecker tests whether a remote IP is allowed to send PROXY headers.
// A nil TrustChecker or empty trusted list trusts everyone — trust control
// is opt-in.
type TrustChecker struct {
	nets []*net.IPNet
	all  bool // true when no networks configured (trust everyone)
}

// NewTrustChecker parses CIDR strings or bare IP addresses into a TrustChecker.
// A bare IP (e.g., "10.0.0.1" or "2001:db8::1") is auto-converted to a /32
// (IPv4) or /128 (IPv6) CIDR. An empty slice returns a trust-everyone checker
// (all=true). Invalid entries return an error.
func NewTrustChecker(cidrs []string) (*TrustChecker, error) {
	if len(cidrs) == 0 {
		return &TrustChecker{all: true}, nil
	}
	nets := make([]*net.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		n, err := parseCIDROrIP(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted-networks entry %q: %w", cidr, err)
		}
		nets[i] = n
	}
	return &TrustChecker{nets: nets}, nil
}

// parseCIDROrIP parses a CIDR string (e.g., "10.0.0.0/8") or a bare IP
// address (e.g., "10.0.0.1"). A bare IPv4 becomes /32, a bare IPv6 becomes
// /128.
func parseCIDROrIP(s string) (*net.IPNet, error) {
	// Try CIDR first — net.ParseCIDR handles both IPv4 and IPv6 CIDR.
	_, n, err := net.ParseCIDR(s)
	if err == nil {
		return n, nil
	}
	// Try bare IP — convert to single-host CIDR.
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("not a valid CIDR or IP address")
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// IsTrusted returns true if ip is in any trusted network. A nil checker or
// all=true trusts everyone.
func (t *TrustChecker) IsTrusted(ip net.IP) bool {
	if t == nil || t.all {
		return true
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
