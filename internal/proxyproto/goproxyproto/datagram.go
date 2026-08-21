// Package goproxyproto datagram adapter — implements proxyproto.DatagramReader
// and proxyproto.DatagramWriter using the go-proxyproto library. Unlike the
// stream reader/writer (which use bufio.Reader/io.Writer), these operate on
// complete []byte datagrams, preserving message boundaries.
package goproxyproto

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

type datagramReader struct{}

// ParseDatagram inspects a UDP datagram for a PROXY Protocol header.
//
//   - No PROXY signature → (zero Header, data, Direct, nil) — direct datagram.
//   - v1/v2 signature + valid header → (Header, payload, V1/V2, nil).
//   - v1/v2 signature but malformed → (zero, nil, 0, err) — caller MUST drop.
//
// Internally uses bufio.Reader to call the library's Read function, but the
// public interface is datagram-oriented ([]byte in, []byte out). The library
// requires *bufio.Reader for its Peek-based parsing; this is an adapter
// implementation detail, not a leaky abstraction.
func (datagramReader) ParseDatagram(data []byte) (pp.Header, []byte, pp.Source, error) {
	if !hasProxySignature(data) {
		// No signature → direct datagram, untouched.
		return pp.Header{}, data, pp.SourceDirect, nil
	}

	// Signature present — parse via library.
	br := bufio.NewReader(bytes.NewReader(data))
	h, err := gop.Read(br)
	if err != nil {
		return pp.Header{}, nil, 0, fmt.Errorf("goproxyproto: malformed PROXY header: %w", err)
	}

	// Read remaining bytes after the header as payload.
	payload, err := io.ReadAll(br)
	if err != nil {
		return pp.Header{}, nil, 0, fmt.Errorf("goproxyproto: reading payload after PROXY header: %w", err)
	}

	// Extract addresses — try TCP then UDP.
	out, err := headerFromGop(h)
	if err != nil {
		return pp.Header{}, nil, 0, err
	}

	src := pp.SourceV1
	if h.Version == 2 {
		src = pp.SourceV2
	}
	return out, payload, src, nil
}

// hasProxySignature checks whether data starts with a PROXY Protocol v1 or v2
// signature. v1 starts with "PROXY " (0x50...); v2 starts with \r\n\r\n\0\r\nQUIT\n.
func hasProxySignature(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// v2 binary signature (first byte is 0x0d = '\r')
	if data[0] == 0x0d && len(data) >= len(gop.SIGV2) {
		return bytes.Equal(data[:len(gop.SIGV2)], gop.SIGV2)
	}
	// v1 text signature (starts with "PROXY ")
	if data[0] == 'P' && len(data) >= 6 {
		return string(data[:6]) == "PROXY "
	}
	return false
}

// headerFromGop converts a library *Header to our proxyproto.Header, trying
// TCP addresses first, then UDP.
func headerFromGop(h *gop.Header) (pp.Header, error) {
	if s, d, ok := h.TCPAddrs(); ok {
		return pp.HeaderFromAddrs(s, d), nil
	}
	if s, d, ok := h.UDPAddrs(); ok {
		return pp.HeaderFromAddrs(s, d), nil
	}
	return pp.Header{}, errors.New("goproxyproto: non-TCP/UDP PROXY header not supported")
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
