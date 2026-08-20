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
