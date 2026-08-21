package goproxyproto

import (
	"bytes"
	"net"
	"testing"

	pp "proxydge/internal/proxyproto"
)

// --- ParseDatagram tests ---

func TestParseDatagramDirect(t *testing.T) {
	data := []byte("hello world")
	hdr, payload, src, err := NewDatagramReader().ParseDatagram(data)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if src != pp.SourceDirect {
		t.Fatalf("src: want Direct, got %v", src)
	}
	if !bytes.Equal(payload, data) {
		t.Fatalf("payload: want %q, got %q", data, payload)
	}
	if hdr.Family != pp.FamilyUnspec {
		t.Fatalf("family: want Unspec, got %v", hdr.Family)
	}
}

func TestParseDatagramV2UDP4(t *testing.T) {
	// Build a PROXY v2 UDP4 datagram
	hdr := pp.Header{
		SrcIP:   net.IPv4(192, 0, 2, 1),
		DstIP:   net.IPv4(198, 51, 100, 1),
		SrcPort: 1234,
		DstPort: 8080,
		Family:  pp.FamilyUDP4,
	}
	payload := []byte("PING")
	encoded, err := NewDatagramWriter().FormatDatagram(hdr, payload)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	parsedHdr, parsedPayload, src, err := NewDatagramReader().ParseDatagram(encoded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if src != pp.SourceV2 {
		t.Fatalf("src: want V2, got %v", src)
	}
	if !bytes.Equal(parsedPayload, payload) {
		t.Fatalf("payload: want %q, got %q", payload, parsedPayload)
	}
	if parsedHdr.Family != pp.FamilyUDP4 {
		t.Fatalf("family: want UDP4, got %v", parsedHdr.Family)
	}
	if !parsedHdr.SrcIP.Equal(hdr.SrcIP) {
		t.Fatalf("srcIP: want %s, got %s", hdr.SrcIP, parsedHdr.SrcIP)
	}
	if !parsedHdr.DstIP.Equal(hdr.DstIP) {
		t.Fatalf("dstIP: want %s, got %s", hdr.DstIP, parsedHdr.DstIP)
	}
	if parsedHdr.SrcPort != hdr.SrcPort {
		t.Fatalf("srcPort: want %d, got %d", hdr.SrcPort, parsedHdr.SrcPort)
	}
	if parsedHdr.DstPort != hdr.DstPort {
		t.Fatalf("dstPort: want %d, got %d", hdr.DstPort, parsedHdr.DstPort)
	}
}

func TestParseDatagramV2TCP4(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.IPv4(10, 0, 0, 1),
		DstIP:   net.IPv4(10, 0, 0, 2),
		SrcPort: 5000,
		DstPort: 9000,
		Family:  pp.FamilyTCP4,
	}
	payload := []byte("TCPDATA")
	encoded, _ := NewDatagramWriter().FormatDatagram(hdr, payload)

	parsedHdr, parsedPayload, src, err := NewDatagramReader().ParseDatagram(encoded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if src != pp.SourceV2 {
		t.Fatalf("src: want V2, got %v", src)
	}
	if parsedHdr.Family != pp.FamilyTCP4 {
		t.Fatalf("family: want TCP4, got %v", parsedHdr.Family)
	}
	if !bytes.Equal(parsedPayload, payload) {
		t.Fatalf("payload: want %q, got %q", payload, parsedPayload)
	}
}

func TestParseDatagramV2UDP6(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.ParseIP("2001:db8::1"),
		DstIP:   net.ParseIP("2001:db8::2"),
		SrcPort: 1234,
		DstPort: 8080,
		Family:  pp.FamilyUDP6,
	}
	payload := []byte("v6DATA")
	encoded, _ := NewDatagramWriter().FormatDatagram(hdr, payload)

	parsedHdr, parsedPayload, src, err := NewDatagramReader().ParseDatagram(encoded)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if src != pp.SourceV2 {
		t.Fatalf("src: want V2, got %v", src)
	}
	if parsedHdr.Family != pp.FamilyUDP6 {
		t.Fatalf("family: want UDP6, got %v", parsedHdr.Family)
	}
	if !parsedHdr.SrcIP.Equal(hdr.SrcIP) {
		t.Fatalf("srcIP: want %s, got %s", hdr.SrcIP, parsedHdr.SrcIP)
	}
	if !bytes.Equal(parsedPayload, payload) {
		t.Fatalf("payload: want %q, got %q", payload, parsedPayload)
	}
}

func TestParseDatagramMalformed(t *testing.T) {
	// PROXY v2 signature + garbage (not enough bytes for header)
	sig := []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}
	malformed := append(sig, 0xFF, 0xFF) // only 2 bytes after sig — need at least 4
	_, _, _, err := NewDatagramReader().ParseDatagram(malformed)
	if err == nil {
		t.Fatal("malformed datagram should return error")
	}
}

func TestParseDatagramMalformedBadLength(t *testing.T) {
	// Valid sig + ver+cmd + fam+proto, but length field says 9999 (exceeds datagram)
	sig := []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}
	hdr := append(sig, 0x21, 0x12, 0x27, 0x0F) // ver=2, cmd=1, fam=UDP4, length=9999
	hdr = append(hdr, bytes.Repeat([]byte{0x00}, 20)...) // some address data (not 9999)
	_, _, _, err := NewDatagramReader().ParseDatagram(hdr)
	if err == nil {
		t.Fatal("bad length should return error")
	}
}

func TestParseDatagramV1Rejected(t *testing.T) {
	v1 := []byte("PROXY TCP4 192.0.2.1 198.51.100.1 1234 8080\r\n")
	_, _, _, err := NewDatagramReader().ParseDatagram(v1)
	if err == nil {
		t.Fatal("v1 should be rejected for UDP")
	}
}

// --- FormatDatagram tests ---

func TestFormatDatagramEveryDatagram(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.IPv4(192, 0, 2, 1),
		DstIP:   net.IPv4(198, 51, 100, 1),
		SrcPort: 1234,
		DstPort: 8080,
		Family:  pp.FamilyUDP4,
	}
	payload := []byte("TEST")
	encoded, err := NewDatagramWriter().FormatDatagram(hdr, payload)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	// Should start with PROXY v2 signature
	sig := []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}
	if !bytes.HasPrefix(encoded, sig) {
		t.Fatal("encoded should start with PROXY v2 signature")
	}
	// Should end with payload
	if !bytes.HasSuffix(encoded, payload) {
		t.Fatal("encoded should end with payload")
	}
	// Payload should be unchanged
	parsedHdr, parsedPayload, _, err := NewDatagramReader().ParseDatagram(encoded)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if !bytes.Equal(parsedPayload, payload) {
		t.Fatalf("round-trip payload: want %q, got %q", payload, parsedPayload)
	}
	if !parsedHdr.SrcIP.Equal(hdr.SrcIP) {
		t.Fatalf("round-trip srcIP: want %s, got %s", hdr.SrcIP, parsedHdr.SrcIP)
	}
}

func TestFormatDatagramFreshlyAllocated(t *testing.T) {
	hdr := pp.Header{
		SrcIP:   net.IPv4(192, 0, 2, 1),
		DstIP:   net.IPv4(198, 51, 100, 1),
		SrcPort: 1234,
		DstPort: 8080,
		Family:  pp.FamilyUDP4,
	}
	encoded1, _ := NewDatagramWriter().FormatDatagram(hdr, []byte("A"))
	encoded2, _ := NewDatagramWriter().FormatDatagram(hdr, []byte("B"))
	// Modify encoded1 — encoded2 should be unaffected
	encoded1[0] = 0xFF
	if encoded2[0] == 0xFF {
		t.Fatal("FormatDatagram should return freshly allocated slices (no shared buffer)")
	}
}
