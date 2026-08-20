// Package proxyproto defines the gateway's own PROXY Protocol abstraction.
// Business code depends only on these types and interfaces; concrete
// wire-format parsing/serialization lives behind an adapter (e.g.
// internal/proxyproto/goproxyproto) and may be swapped without touching
// the gateway.
package proxyproto

import (
	"bufio"
	"io"
	"net"
)

// Family is the PROXY Protocol address family.
type Family int

const (
	FamilyUnspec Family = iota
	FamilyTCP4
	FamilyTCP6
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

// AddrConn is the minimal address surface HeaderFromConn needs. transport.Conn
// satisfies it, so proxyproto does not depend on transport (no import cycle).
type AddrConn interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// Reader detects and parses a (possibly absent) PROXY Protocol header from br.
//   - err == nil, src == SourceDirect: no header present (direct connection);
//     hdr is the zero value and no bytes were consumed by detection.
//   - err == nil, src == SourceV1 or SourceV2: a valid header was parsed; the
//     consumed header bytes have been read from br.
//   - err != nil: a header prefix matched but parsing failed (malformed); the
//     gateway must close the connection.
type Reader interface {
	Read(br *bufio.Reader) (hdr Header, src Source, err error)
}

// Writer writes a PROXY Protocol v2 header for hdr to w.
type Writer interface {
	WriteTo(w io.Writer, hdr Header) error
}

// HeaderFromConn builds a Header from a direct connection's real socket
// addresses: Src is the peer (RemoteAddr), Dst is the listener (LocalAddr).
// Direct connections never emit UNSPEC — the socket addresses are the real
// client information the gateway exists to preserve. Non-TCP addresses (which
// cannot yield IP/port) fall back to FamilyUnspec.
func HeaderFromConn(c AddrConn) Header {
	lt, ok1 := c.LocalAddr().(*net.TCPAddr)
	rt, ok2 := c.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return Header{Family: FamilyUnspec}
	}
	h := Header{SrcPort: uint16(rt.Port), DstPort: uint16(lt.Port)}
	if v4 := rt.IP.To4(); v4 != nil {
		h.SrcIP, h.DstIP = v4, lt.IP.To4()
		h.Family = FamilyTCP4
	} else {
		h.SrcIP, h.DstIP = rt.IP, lt.IP
		h.Family = FamilyTCP6
	}
	return h
}
