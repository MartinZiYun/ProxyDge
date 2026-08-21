package gateway

import (
	"net"
	"testing"

	"proxydge/internal/proxyproto"
	"proxydge/internal/transport"
)

type fakeAddrConn struct {
	local, remote net.Addr
}

func (f fakeAddrConn) LocalAddr() net.Addr  { return f.local }
func (f fakeAddrConn) RemoteAddr() net.Addr { return f.remote }

func testAddrConn() transport.AddrConn {
	return fakeAddrConn{
		local:  &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 8080},
		remote: &net.TCPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 1234},
	}
}

func TestDecideAllowDirect(t *testing.T) {
	trust, _ := NewTrustChecker(nil) // trust everyone
	hdr, src, allow, reason := Decide(PolicyUse, trust, UntrustedReject,
		proxyproto.SourceDirect, proxyproto.Header{}, net.IPv4(127, 0, 0, 1), testAddrConn())
	if !allow || reason != "" {
		t.Fatalf("want allow/reason=\"\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceDirect {
		t.Fatalf("src: want direct, got %v", src)
	}
	// hdr should be rebuilt from socket (not zero value)
	if hdr.Family == proxyproto.FamilyUnspec {
		t.Fatalf("hdr should be rebuilt from socket, got FamilyUnspec")
	}
	if !hdr.SrcIP.Equal(net.IPv4(198, 51, 100, 1)) {
		t.Fatalf("hdr SrcIP: want 198.51.100.1 (socket remote), got %s", hdr.SrcIP)
	}
}

func TestDecideAllowProxyTrusted(t *testing.T) {
	trust, _ := NewTrustChecker(nil)
	inHdr := proxyproto.Header{SrcIP: net.IPv4(10, 0, 0, 1), SrcPort: 1234, Family: proxyproto.FamilyTCP4}
	hdr, src, allow, reason := Decide(PolicyUse, trust, UntrustedReject,
		proxyproto.SourceV2, inHdr, net.IPv4(127, 0, 0, 1), testAddrConn())
	if !allow || reason != "" {
		t.Fatalf("want allow/reason=\"\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceV2 {
		t.Fatalf("src: want v2, got %v", src)
	}
	// hdr should be the original PROXY header, not rebuilt
	if !hdr.SrcIP.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Fatalf("hdr should preserve original SrcIP, got %s", hdr.SrcIP)
	}
}

func TestDecideUntrustedReject(t *testing.T) {
	trust, _ := NewTrustChecker([]string{"10.0.0.0/8"}) // 127.x not trusted
	hdr, src, allow, reason := Decide(PolicyUse, trust, UntrustedReject,
		proxyproto.SourceV2, proxyproto.Header{}, net.IPv4(127, 0, 0, 1), testAddrConn())
	if allow || reason != "untrusted" {
		t.Fatalf("want !allow/reason=\"untrusted\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceV2 {
		t.Fatalf("src should be unchanged, got %v", src)
	}
	_ = hdr
}

func TestDecideUntrustedStrip(t *testing.T) {
	trust, _ := NewTrustChecker([]string{"10.0.0.0/8"}) // 127.x not trusted
	inHdr := proxyproto.Header{SrcIP: net.IPv4(10, 0, 0, 1), SrcPort: 1234, Family: proxyproto.FamilyTCP4}
	hdr, src, allow, reason := Decide(PolicyUse, trust, UntrustedStrip,
		proxyproto.SourceV2, inHdr, net.IPv4(127, 0, 0, 1), testAddrConn())
	if !allow || reason != "strip" {
		t.Fatalf("want allow/reason=\"strip\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceDirect {
		t.Fatalf("src: want direct (stripped), got %v", src)
	}
	// hdr should be rebuilt from socket, not the original PROXY header
	if hdr.SrcIP.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Fatalf("hdr should be rebuilt from socket, not preserve original SrcIP")
	}
	if !hdr.SrcIP.Equal(net.IPv4(198, 51, 100, 1)) {
		t.Fatalf("hdr SrcIP: want 198.51.100.1 (socket remote), got %s", hdr.SrcIP)
	}
}

func TestDecidePolicyForbids(t *testing.T) {
	trust, _ := NewTrustChecker(nil)
	hdr, src, allow, reason := Decide(PolicyReject, trust, UntrustedReject,
		proxyproto.SourceV2, proxyproto.Header{}, net.IPv4(127, 0, 0, 1), testAddrConn())
	if allow || reason != "policy:forbids" {
		t.Fatalf("want !allow/reason=\"policy:forbids\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceV2 {
		t.Fatalf("src should be unchanged, got %v", src)
	}
	_ = hdr
}

func TestDecidePolicyRequires(t *testing.T) {
	trust, _ := NewTrustChecker(nil)
	hdr, src, allow, reason := Decide(PolicyRequire, trust, UntrustedReject,
		proxyproto.SourceDirect, proxyproto.Header{}, net.IPv4(127, 0, 0, 1), testAddrConn())
	if allow || reason != "policy:requires" {
		t.Fatalf("want !allow/reason=\"policy:requires\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceDirect {
		t.Fatalf("src should be unchanged, got %v", src)
	}
	_ = hdr
}

func TestDecideDirectAllowedUnderReject(t *testing.T) {
	trust, _ := NewTrustChecker(nil)
	hdr, src, allow, reason := Decide(PolicyReject, trust, UntrustedReject,
		proxyproto.SourceDirect, proxyproto.Header{}, net.IPv4(127, 0, 0, 1), testAddrConn())
	if !allow || reason != "" {
		t.Fatalf("want allow/reason=\"\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceDirect {
		t.Fatalf("src: want direct, got %v", src)
	}
	_ = hdr
}

func TestDecideProxyAllowedUnderRequire(t *testing.T) {
	trust, _ := NewTrustChecker(nil)
	inHdr := proxyproto.Header{SrcIP: net.IPv4(10, 0, 0, 1), SrcPort: 1234, Family: proxyproto.FamilyTCP4}
	hdr, src, allow, reason := Decide(PolicyRequire, trust, UntrustedReject,
		proxyproto.SourceV2, inHdr, net.IPv4(127, 0, 0, 1), testAddrConn())
	if !allow || reason != "" {
		t.Fatalf("want allow/reason=\"\", got allow=%v reason=%q", allow, reason)
	}
	if src != proxyproto.SourceV2 {
		t.Fatalf("src: want v2, got %v", src)
	}
	_ = hdr
}
