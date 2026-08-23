// Package proxyproto defines the gateway's own PROXY Protocol abstraction.
//
// Business code depends only on these types and interfaces; concrete
// wire-format parsing/serialization lives behind an adapter (e.g.
// internal/proxyproto/goproxyproto) and may be swapped without touching
// the gateway.
//
// Two families of interfaces exist:
//   - Stream (TCP): Reader/Writer operate on *bufio.Reader / io.Writer.
//   - Datagram (UDP): DatagramReader/DatagramWriter operate on []byte,
//     preserving datagram boundaries. UDP must NOT use the stream interfaces.
package proxyproto

import (
	"bufio"
	"io"
	"net"

	"proxydge/internal/transport"
)

// Family is the PROXY Protocol address family.
type Family int

const (
	FamilyUnspec Family = iota
	FamilyTCP4
	FamilyTCP6
	FamilyUDP4 // AF_INET + SOCK_DGRAM
	FamilyUDP6 // AF_INET6 + SOCK_DGRAM
)

// TLV is a PROXY v2 type-length-value entry. The first version does not
// populate or emit TLVs; the field is reserved so later TLV forwarding
// can be added without changing the Header shape.
type TLV struct {
	Type  byte
	Value []byte
}

// Header carries the source/destination addresses that a PROXY header
// (or a direct connection's socket addresses) describe.
type Header struct {
	SrcIP, DstIP     net.IP
	SrcPort, DstPort uint16
	Family           Family
	TLVs             []TLV
}

// Source indicates how a connection's addresses were obtained.
type Source int

const (
	SourceDirect Source = iota // no PROXY header; addresses came from the socket
	SourceV1                   // PROXY Protocol v1 text header
	SourceV2                   // PROXY Protocol v2 binary header
)

// String renders Source for logging. slog formats values via fmt, which calls
// this method on the Stringer, so log lines read "direct"/"v1"/"v2".
func (s Source) String() string {
	switch s {
	case SourceDirect:
		return "direct"
	case SourceV1:
		return "v1"
	case SourceV2:
		return "v2"
	}
	return "unknown"
}

// Reader detects and parses a (possibly absent) PROXY Protocol header from br.
// This is the TCP stream-oriented interface. UDP must use DatagramReader
// instead.
//
//   - err == nil, src == SourceDirect: no header present (direct connection);
//     hdr is the zero value and no bytes were consumed by detection.
//   - err == nil, src == SourceV1 or SourceV2: a valid header was parsed; the
//     consumed header bytes have been read from br.
//   - err != nil: a header prefix matched but parsing failed (malformed); the
//     gateway must close the connection.
type Reader interface {
	Read(br *bufio.Reader) (hdr Header, src Source, err error)
}

// Writer writes a PROXY Protocol header for hdr to w, in the wire version the
// implementation was constructed with (see goproxyproto.NewWriter). A zero
// Header (FamilyUnspec) requests the protocol's address-unknown form:
// "PROXY UNKNOWN" (v1) or LOCAL+AF_UNSPEC (v2).
// This is the TCP stream-oriented interface. UDP must use DatagramWriter
// instead.
type Writer interface {
	WriteTo(w io.Writer, hdr Header) error
}

// DatagramReader parses a UDP datagram, detecting and extracting a PROXY v2
// header if present. Unlike the stream Reader, it operates on complete
// datagrams — no bufio.Reader, no partial reads, no detection timeout.
//
//   - data has PROXY v2 signature + valid header → (hdr, payload, V2, nil)
//   - data has no signature → (zero Header, data, Direct, nil)
//   - data has signature but malformed → (zero, nil, 0, err) — caller MUST drop
type DatagramReader interface {
	ParseDatagram(data []byte) (hdr Header, payload []byte, src Source, err error)
}

// DatagramWriter encodes a PROXY v2 header + payload into a complete datagram.
// The returned []byte is a freshly allocated buffer (caller may modify freely).
type DatagramWriter interface {
	FormatDatagram(hdr Header, payload []byte) ([]byte, error)
}

// HeaderFromAddrs builds a Header from source and destination addresses.
// Handles both TCP (*net.TCPAddr → FamilyTCP4/TCP6) and UDP
// (*net.UDPAddr → FamilyUDP4/UDP6) address types. Non-matching or
// mismatched types fall back to FamilyUnspec.
//
// This is the preferred construction path for both TCP and UDP. TCP callers
// can use HeaderFromConn (which delegates here) or call directly.
// UDP callers should call this directly with the addresses from
// ReadFromUDP and listener.LocalAddr — do NOT create a fake Conn.
func HeaderFromAddrs(src, dst net.Addr) Header {
	switch s := src.(type) {
	case *net.TCPAddr:
		d, ok := dst.(*net.TCPAddr)
		if !ok {
			return Header{Family: FamilyUnspec}
		}
		return buildAddrHeader(s.IP, s.Port, d.IP, d.Port, FamilyTCP4, FamilyTCP6)
	case *net.UDPAddr:
		d, ok := dst.(*net.UDPAddr)
		if !ok {
			return Header{Family: FamilyUnspec}
		}
		return buildAddrHeader(s.IP, s.Port, d.IP, d.Port, FamilyUDP4, FamilyUDP6)
	}
	return Header{Family: FamilyUnspec}
}

// buildAddrHeader constructs a Header from IP/port pairs, selecting the
// v4 or v6 family based on whether the source IP is IPv4.
func buildAddrHeader(srcIP net.IP, srcPort int, dstIP net.IP, dstPort int, famV4, famV6 Family) Header {
	h := Header{SrcPort: uint16(srcPort), DstPort: uint16(dstPort)}
	if v4 := srcIP.To4(); v4 != nil {
		h.SrcIP, h.DstIP = v4, dstIP.To4()
		h.Family = famV4
	} else {
		h.SrcIP, h.DstIP = srcIP, dstIP
		h.Family = famV6
	}
	return h
}

// HeaderFromConn builds a Header from a direct connection's real socket
// addresses. Delegates to HeaderFromAddrs. Kept for TCP backward
// compatibility; new code should call HeaderFromAddrs directly.
func HeaderFromConn(c transport.AddrConn) Header {
	return HeaderFromAddrs(c.RemoteAddr(), c.LocalAddr())
}
