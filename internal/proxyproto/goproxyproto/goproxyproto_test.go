package goproxyproto

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

// tcp4Header is the spec-derived PROXY v2 header for
// Src=192.0.2.1:1234, Dst=198.51.100.1:8080 (TCP4). Used both as parse input
// and as the writer's expected output — an independent oracle, not produced by
// the library.
const tcp4Header = "0d0a0d0a000d0a515549540a2111000cc0000201c633640104d21f90"

const tcp6Header = "0d0a0d0a000d0a515549540a2121002420010db800000000000000000000000120010db800000000000000000000000204d21f90"

const appData = "APPDATA"

func TestReaderParseV2(t *testing.T) {
	in := append(mustHex(t, tcp4Header), []byte(appData)...)
	br := bufio.NewReader(bytes.NewReader(in))
	r := NewReader()
	h, src, err := r.Read(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if src != pp.SourceV2 {
		t.Fatalf("source: want SourceV2, got %v", src)
	}
	if h.Family != pp.FamilyTCP4 {
		t.Fatalf("family: want FamilyTCP4, got %v", h.Family)
	}
	if got, want := h.SrcIP.String(), "192.0.2.1"; got != want {
		t.Fatalf("src ip: want %s, got %s", want, got)
	}
	if h.SrcPort != 1234 {
		t.Fatalf("src port: want 1234, got %d", h.SrcPort)
	}
	if got, want := h.DstIP.String(), "198.51.100.1"; got != want {
		t.Fatalf("dst ip: want %s, got %s", want, got)
	}
	if h.DstPort != 8080 {
		t.Fatalf("dst port: want 8080, got %d", h.DstPort)
	}
	// Application data after the header must still be available.
	rest, _ := io.ReadAll(br)
	if got, want := string(rest), appData; got != want {
		t.Fatalf("app data: want %q, got %q", want, got)
	}
}

func TestReaderParseV1(t *testing.T) {
	v1 := []byte("PROXY TCP4 192.0.2.1 198.51.100.1 1234 8080\r\n" + appData)
	br := bufio.NewReader(bytes.NewReader(v1))
	r := NewReader()
	h, src, err := r.Read(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if src != pp.SourceV1 {
		t.Fatalf("source: want SourceV1, got %v", src)
	}
	if h.Family != pp.FamilyTCP4 || h.SrcIP.String() != "192.0.2.1" || h.DstIP.String() != "198.51.100.1" || h.SrcPort != 1234 || h.DstPort != 8080 {
		t.Fatalf("header mismatch: %+v", h)
	}
	rest, _ := io.ReadAll(br)
	if got, want := string(rest), appData; got != want {
		t.Fatalf("app data: want %q, got %q", want, got)
	}
}

func TestReaderDirectNoConsume(t *testing.T) {
	// First byte 0x01 is neither 'P' nor '\r': the library's Peek(1) fast-path
	// returns ErrNoProxyProtocol without consuming any bytes.
	in := []byte{0x01, 0x02, 0x03, 'h', 'e', 'l', 'l', 'o'}
	br := bufio.NewReader(bytes.NewReader(in))
	r := NewReader()
	h, src, err := r.Read(br)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if src != pp.SourceDirect {
		t.Fatalf("source: want SourceDirect, got %v", src)
	}
	if h.Family != pp.FamilyUnspec {
		t.Fatalf("direct hdr should be zero, family=%v", h.Family)
	}
	rest, _ := io.ReadAll(br)
	if got, want := rest, in; !bytes.Equal(got, want) {
		t.Fatalf("bytes consumed: want %v preserved, got %v", want, got)
	}
}

func TestReaderMalformed(t *testing.T) {
	// Signature prefix matches ("PROXY ") but the payload is invalid (port
	// 99999 > 65535): the library must return a non-NoProxyProtocol error.
	v1 := []byte("PROXY TCP4 1.2.3.4 5.6.7.8 99999 2\r\n")
	br := bufio.NewReader(bytes.NewReader(v1))
	r := NewReader()
	_, _, err := r.Read(br)
	if err == nil {
		t.Fatalf("expected malformed error, got nil")
	}
	if errors.Is(err, gop.ErrNoProxyProtocol) {
		t.Fatalf("malformed must not be reported as ErrNoProxyProtocol: %v", err)
	}
}

func TestWriterTCP4(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.IPv4(192, 0, 2, 1),
		SrcPort: 1234,
		DstIP:   net.IPv4(198, 51, 100, 1),
		DstPort: 8080,
		Family:  pp.FamilyTCP4,
	}
	var buf bytes.Buffer
	if err := NewWriter().WriteTo(&buf, hdr); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := mustHex(t, tcp4Header)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("output:\nwant %x\n got %x", want, got)
	}
}

func TestWriterTCP6(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.ParseIP("2001:db8::1"),
		SrcPort: 1234,
		DstIP:   net.ParseIP("2001:db8::2"),
		DstPort: 8080,
		Family:  pp.FamilyTCP6,
	}
	var buf bytes.Buffer
	if err := NewWriter().WriteTo(&buf, hdr); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := mustHex(t, tcp6Header)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("output:\nwant %x\n got %x", want, got)
	}
}
