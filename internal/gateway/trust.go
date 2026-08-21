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

// NewTrustChecker parses CIDR strings into a TrustChecker. An empty slice
// returns a trust-everyone checker (all=true). Invalid CIDRs return an error.
func NewTrustChecker(cidrs []string) (*TrustChecker, error) {
	if len(cidrs) == 0 {
		return &TrustChecker{all: true}, nil
	}
	nets := make([]*net.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted-networks entry %q: %w", cidr, err)
		}
		nets[i] = n
	}
	return &TrustChecker{nets: nets}, nil
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
