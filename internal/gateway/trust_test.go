package gateway

import (
	"net"
	"testing"
)

func TestTrustCheckerNilIsTrusted(t *testing.T) {
	var tc *TrustChecker
	if !tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("nil TrustChecker should trust everyone")
	}
}

func TestTrustCheckerEmptyTrustsAll(t *testing.T) {
	tc, err := NewTrustChecker(nil)
	if err != nil {
		t.Fatalf("NewTrustChecker(nil): %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("empty trusted list should trust everyone")
	}
}

func TestTrustCheckerSingleCIDR(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("10.1.2.3 should be trusted in 10.0.0.0/8")
	}
	if tc.IsTrusted(net.ParseIP("192.168.1.1")) {
		t.Fatal("192.168.1.1 should not be trusted in 10.0.0.0/8")
	}
}

func TestTrustCheckerMultipleCIDRs(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("10.1.2.3 should match first CIDR")
	}
	if !tc.IsTrusted(net.ParseIP("192.168.1.1")) {
		t.Fatal("192.168.1.1 should match second CIDR")
	}
	if tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("8.8.8.8 should not match any CIDR")
	}
}

func TestTrustCheckerIPv4MappedIPv6(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	ip := net.ParseIP("::ffff:10.0.0.1")
	if ip == nil {
		t.Fatal("failed to parse ::ffff:10.0.0.1")
	}
	if !tc.IsTrusted(ip) {
		t.Fatal("::ffff:10.0.0.1 should match 10.0.0.0/8 (IPv4-mapped IPv6)")
	}
}

func TestTrustCheckerIPv6CIDR(t *testing.T) {
	tc, err := NewTrustChecker([]string{"2001:db8::/32"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("2001:db8::1")) {
		t.Fatal("2001:db8::1 should be trusted in 2001:db8::/32")
	}
	if !tc.IsTrusted(net.ParseIP("2001:db8:abcd:ef12::1")) {
		t.Fatal("2001:db8:abcd:ef12::1 should be trusted in 2001:db8::/32")
	}
	if tc.IsTrusted(net.ParseIP("2001:db9::1")) {
		t.Fatal("2001:db9::1 should NOT be trusted in 2001:db8::/32")
	}
	if tc.IsTrusted(net.ParseIP("10.0.0.1")) {
		t.Fatal("10.0.0.1 should NOT be trusted in 2001:db8::/32")
	}
}

func TestTrustCheckerIPv6LinkLocalCIDR(t *testing.T) {
	tc, err := NewTrustChecker([]string{"fe80::/10"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("fe80::1")) {
		t.Fatal("fe80::1 should be trusted in fe80::/10")
	}
	if !tc.IsTrusted(net.ParseIP("febf::1")) {
		t.Fatal("febf::1 should be trusted in fe80::/10")
	}
	if tc.IsTrusted(net.ParseIP("fec0::1")) {
		t.Fatal("fec0::1 should NOT be trusted in fe80::/10")
	}
}

func TestTrustCheckerBareIPv4(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.1"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.0.0.1")) {
		t.Fatal("10.0.0.1 should be trusted (bare IP = /32)")
	}
	if tc.IsTrusted(net.ParseIP("10.0.0.2")) {
		t.Fatal("10.0.0.2 should NOT be trusted (bare IP is /32, not /24)")
	}
}

func TestTrustCheckerBareIPv6(t *testing.T) {
	tc, err := NewTrustChecker([]string{"2001:db8::1"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("2001:db8::1")) {
		t.Fatal("2001:db8::1 should be trusted (bare IP = /128)")
	}
	if tc.IsTrusted(net.ParseIP("2001:db8::2")) {
		t.Fatal("2001:db8::2 should NOT be trusted (bare IP is /128)")
	}
}

func TestTrustCheckerMixedCIDRAndBareIP(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8", "2001:db8::1", "::ffff:192.168.1.0/120"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("10.1.2.3 should match 10.0.0.0/8")
	}
	if !tc.IsTrusted(net.ParseIP("2001:db8::1")) {
		t.Fatal("2001:db8::1 should match bare IP")
	}
	if tc.IsTrusted(net.ParseIP("2001:db8::2")) {
		t.Fatal("2001:db8::2 should NOT match (bare /128)")
	}
}

func TestNewTrustCheckerInvalidCIDR(t *testing.T) {
	_, err := NewTrustChecker([]string{"not-a-cidr"})
	if err == nil {
		t.Fatal("invalid CIDR should return error")
	}
}

func TestUntrustedActionString(t *testing.T) {
	if UntrustedReject.String() != "reject" {
		t.Fatalf("UntrustedReject.String(): want reject, got %q", UntrustedReject.String())
	}
	if UntrustedStrip.String() != "strip" {
		t.Fatalf("UntrustedStrip.String(): want strip, got %q", UntrustedStrip.String())
	}
}
