package proxyproto

import (
	"net"
	"testing"
)

type fakeAddrConn struct {
	local, remote net.Addr
}

func (f fakeAddrConn) LocalAddr() net.Addr  { return f.local }
func (f fakeAddrConn) RemoteAddr() net.Addr { return f.remote }

func TestHeaderFromConnTCP4(t *testing.T) {
	c := fakeAddrConn{
		local:  &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 8080},
		remote: &net.TCPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 1234},
	}
	h := HeaderFromConn(c)
	if h.Family != FamilyTCP4 {
		t.Fatalf("family: want FamilyTCP4, got %v", h.Family)
	}
	if got, want := h.SrcIP.String(), "198.51.100.1"; got != want {
		t.Fatalf("src ip: want %s, got %s", want, got)
	}
	if h.SrcPort != 1234 {
		t.Fatalf("src port: want 1234, got %d", h.SrcPort)
	}
	if got, want := h.DstIP.String(), "192.0.2.1"; got != want {
		t.Fatalf("dst ip: want %s, got %s", want, got)
	}
	if h.DstPort != 8080 {
		t.Fatalf("dst port: want 8080, got %d", h.DstPort)
	}
}

func TestHeaderFromConnTCP6(t *testing.T) {
	c := fakeAddrConn{
		local:  &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 8080},
		remote: &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 1234},
	}
	h := HeaderFromConn(c)
	if h.Family != FamilyTCP6 {
		t.Fatalf("family: want FamilyTCP6, got %v", h.Family)
	}
	if got, want := h.SrcIP.String(), "2001:db8::2"; got != want {
		t.Fatalf("src ip: want %s, got %s", want, got)
	}
	if h.SrcPort != 1234 {
		t.Fatalf("src port: want 1234, got %d", h.SrcPort)
	}
	if got, want := h.DstIP.String(), "2001:db8::1"; got != want {
		t.Fatalf("dst ip: want %s, got %s", want, got)
	}
	if h.DstPort != 8080 {
		t.Fatalf("dst port: want 8080, got %d", h.DstPort)
	}
}

func TestHeaderFromConnNonTCP(t *testing.T) {
	// A non-TCP address (e.g. unix) cannot yield IP/port → UNSPEC.
	c := fakeAddrConn{
		local:  &net.UnixAddr{Name: "/tmp/x", Net: "unix"},
		remote: &net.UnixAddr{Name: "/tmp/y", Net: "unix"},
	}
	h := HeaderFromConn(c)
	if h.Family != FamilyUnspec {
		t.Fatalf("family: want FamilyUnspec for non-TCP, got %v", h.Family)
	}
}

// --- FamilyMatchesAddrs: per-address consistency with the declared family ---

func TestFamilyMatchesAddrsConsistent(t *testing.T) {
	ok := []Header{
		// Declared TCP4, both addresses genuinely IPv4.
		{Family: FamilyTCP4, SrcIP: net.IPv4(192, 0, 2, 1), DstIP: net.IPv4(198, 51, 100, 1)},
		// Declared TCP6, both addresses pure IPv6 (To4() == nil).
		{Family: FamilyTCP6, SrcIP: net.ParseIP("2001:db8::1"), DstIP: net.ParseIP("2001:db8::2")},
		// Unspec claims nothing — vacuously consistent (the unknown-rewrite form).
		{Family: FamilyUnspec},
	}
	for i, h := range ok {
		if !FamilyMatchesAddrs(h) {
			t.Fatalf("case %d: want consistent, got violation (family=%d)", i, h.Family)
		}
	}
}

func TestFamilyMatchesAddrsViolations(t *testing.T) {
	mapped := net.ParseIP("::ffff:192.168.1.1") // 16 bytes, but To4() != nil
	bad := []struct {
		name string
		h    Header
	}{
		{
			// The one shape craftable on the wire: INET6 declared, dst is a
			// mapped IPv4. Both addresses are 16 bytes — only the per-address
			// family check catches it; a src/dst mutual comparison would not.
			name: "tcp6 with mapped-v4 dst",
			h:    Header{Family: FamilyTCP6, SrcIP: net.ParseIP("2001:db8::1"), DstIP: mapped},
		},
		{
			name: "tcp6 with mapped-v4 src",
			h:    Header{Family: FamilyTCP6, SrcIP: mapped, DstIP: net.ParseIP("2001:db8::1")},
		},
		{
			// nil + nil must NOT vacuously satisfy the TCP6 "both not To4-able"
			// branch — this is exactly the reader-normalized shape for crafted
			// AF_INET6 input with an IPv4-only destination.
			name: "tcp6 with nil dst",
			h:    Header{Family: FamilyTCP6, SrcIP: net.ParseIP("2001:db8::1"), DstIP: nil},
		},
		{
			name: "tcp6 with both nil",
			h:    Header{Family: FamilyTCP6},
		},
		{
			name: "tcp4 with nil dst",
			h:    Header{Family: FamilyTCP4, SrcIP: net.IPv4(192, 0, 2, 1), DstIP: nil},
		},
		{
			name: "tcp4 with pure-v6 dst",
			h:    Header{Family: FamilyTCP4, SrcIP: net.IPv4(192, 0, 2, 1), DstIP: net.ParseIP("2001:db8::1")},
		},
		{
			name: "tcp4 with missing src",
			h:    Header{Family: FamilyTCP4, DstIP: net.IPv4(198, 51, 100, 1)},
		},
	}
	for _, tc := range bad {
		if FamilyMatchesAddrs(tc.h) {
			t.Fatalf("%s: want violation, got consistent", tc.name)
		}
	}
}
