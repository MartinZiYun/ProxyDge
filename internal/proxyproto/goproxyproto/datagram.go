// Package goproxyproto datagram adapter — implements proxyproto.DatagramReader
// and proxyproto.DatagramWriter. Unlike the stream reader/writer (which use
// bufio.Reader/io.Writer), these operate on complete []byte datagrams,
// preserving message boundaries. No bufio.Reader, no bytes.Reader — the v2
// header is parsed directly from the raw datagram bytes.
package goproxyproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

type datagramReader struct{}

// sigV2 is the 12-byte PROXY Protocol v2 magic signature.
var sigV2 = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

// ParseDatagram inspects a UDP datagram for a PROXY Protocol v2 header.
// Parses the header directly from raw bytes — no bufio.Reader, no stream
// abstraction.
//
//   - No PROXY signature → (zero Header, data, Direct, nil) — direct datagram.
//   - v2 signature + valid header → (Header, payload, V2, nil).
//   - v2 signature but malformed → (zero, nil, 0, err) — caller MUST drop.
//   - v1 signature → error (v1 not supported for UDP datagrams).
func (datagramReader) ParseDatagram(data []byte) (pp.Header, []byte, pp.Source, error) {
	if len(data) == 0 {
		return pp.Header{}, data, pp.SourceDirect, nil
	}

	// Check for v2 binary signature.
	if data[0] == 0x0d {
		if len(data) < len(sigV2) || !bytes.Equal(data[:len(sigV2)], sigV2) {
			// Starts with \r but not a valid v2 signature — treat as direct.
			return pp.Header{}, data, pp.SourceDirect, nil
		}
		return parseV2(data)
	}

	// Check for v1 text signature ("PROXY ").
	if data[0] == 'P' && len(data) >= 6 && string(data[:6]) == "PROXY " {
		return pp.Header{}, nil, 0, errors.New("goproxyproto: PROXY v1 not supported for UDP datagrams")
	}

	// No signature → direct datagram.
	return pp.Header{}, data, pp.SourceDirect, nil
}

// parseV2 parses a PROXY Protocol v2 header from a complete datagram.
// Layout: [12 sig][1 ver+cmd][1 fam+proto][2 length][address payload...]
func parseV2(data []byte) (pp.Header, []byte, pp.Source, error) {
	const sigLen = 12
	if len(data) < sigLen+4 {
		return pp.Header{}, nil, 0, errors.New("goproxyproto: datagram too short for PROXY v2 header")
	}

	verCmd := data[sigLen]
	famProto := data[sigLen+1]
	addrLen := int(binary.BigEndian.Uint16(data[sigLen+2 : sigLen+4]))
	headerSize := sigLen + 4 + addrLen

	if headerSize > len(data) {
		return pp.Header{}, nil, 0, errors.New("goproxyproto: PROXY header length exceeds datagram")
	}

	// Validate version (high nibble = 2) and command (low nibble = 1 = PROXY).
	if verCmd>>4 != 2 {
		return pp.Header{}, nil, 0, fmt.Errorf("goproxyproto: unsupported PROXY version %d", verCmd>>4)
	}
	if verCmd&0x0F != 1 {
		return pp.Header{}, nil, 0, fmt.Errorf("goproxyproto: unsupported PROXY command %d (only PROXY supported)", verCmd&0x0F)
	}

	// Parse address family + transport protocol.
	addrStart := sigLen + 4
	addrData := data[addrStart : headerSize]

	var hdr pp.Header
	switch famProto {
	case 0x11: // AF_INET + SOCK_STREAM (TCP4)
		hdr = parseV4Addrs(addrData, pp.FamilyTCP4)
	case 0x21: // AF_INET6 + SOCK_STREAM (TCP6)
		hdr = parseV6Addrs(addrData, pp.FamilyTCP6)
	case 0x12: // AF_INET + SOCK_DGRAM (UDP4)
		hdr = parseV4Addrs(addrData, pp.FamilyUDP4)
	case 0x22: // AF_INET6 + SOCK_DGRAM (UDP6)
		hdr = parseV6Addrs(addrData, pp.FamilyUDP6)
	default:
		return pp.Header{}, nil, 0, fmt.Errorf("goproxyproto: unsupported address family/protocol 0x%02x", famProto)
	}

	if hdr.Family == pp.FamilyUnspec {
		return pp.Header{}, nil, 0, errors.New("goproxyproto: invalid address length for PROXY family")
	}

	payload := data[headerSize:]
	return hdr, payload, pp.SourceV2, nil
}

// parseV4Addrs parses IPv4 addresses: 4+4+2+2 = 12 bytes.
func parseV4Addrs(data []byte, fam pp.Family) pp.Header {
	if len(data) < 12 {
		return pp.Header{Family: pp.FamilyUnspec}
	}
	return pp.Header{
		SrcIP:   append([]byte(nil), data[0:4]...),
		DstIP:   append([]byte(nil), data[4:8]...),
		SrcPort: binary.BigEndian.Uint16(data[8:10]),
		DstPort: binary.BigEndian.Uint16(data[10:12]),
		Family:  fam,
	}
}

// parseV6Addrs parses IPv6 addresses: 16+16+2+2 = 36 bytes.
func parseV6Addrs(data []byte, fam pp.Family) pp.Header {
	if len(data) < 36 {
		return pp.Header{Family: pp.FamilyUnspec}
	}
	return pp.Header{
		SrcIP:   append([]byte(nil), data[0:16]...),
		DstIP:   append([]byte(nil), data[16:32]...),
		SrcPort: binary.BigEndian.Uint16(data[32:34]),
		DstPort: binary.BigEndian.Uint16(data[34:36]),
		Family:  fam,
	}
}

// NewDatagramReader returns a pp.DatagramReader backed by go-proxyproto.
func NewDatagramReader() pp.DatagramReader { return datagramReader{} }

// --- DatagramWriter ---

type datagramWriter struct{}

// FormatDatagram encodes a PROXY v2 header + payload into a single []byte.
// Uses the library's Header.Format() for the header bytes (no io.Writer needed),
// then appends the payload. The returned slice is freshly allocated.
func (datagramWriter) FormatDatagram(hdr pp.Header, payload []byte) ([]byte, error) {
	src, dst := addrsFromHeader(hdr)
	h := gop.HeaderProxyFromAddrs(2, src, dst)
	headerBytes, err := h.Format()
	if err != nil {
		return nil, fmt.Errorf("goproxyproto: format PROXY header: %w", err)
	}
	result := make([]byte, 0, len(headerBytes)+len(payload))
	result = append(result, headerBytes...)
	result = append(result, payload...)
	return result, nil
}

// addrsFromHeader converts our Header to net.Addr pair for the library.
// Uses *net.UDPAddr for UDP families, *net.TCPAddr for TCP families.
// The library's HeaderProxyFromAddrs infers SOCK_DGRAM/SOCK_STREAM from
// the concrete address type.
func addrsFromHeader(hdr pp.Header) (src, dst net.Addr) {
	if hdr.Family == pp.FamilyUDP4 || hdr.Family == pp.FamilyUDP6 {
		return &net.UDPAddr{IP: hdr.SrcIP, Port: int(hdr.SrcPort)},
			&net.UDPAddr{IP: hdr.DstIP, Port: int(hdr.DstPort)}
	}
	return &net.TCPAddr{IP: hdr.SrcIP, Port: int(hdr.SrcPort)},
		&net.TCPAddr{IP: hdr.DstIP, Port: int(hdr.DstPort)}
}

// NewDatagramWriter returns a pp.DatagramWriter backed by go-proxyproto.
func NewDatagramWriter() pp.DatagramWriter { return datagramWriter{} }
