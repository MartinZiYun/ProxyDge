// Package goproxyproto is the only place that imports github.com/pires/go-proxyproto.
// It adapts the library to the gateway's own proxyproto.Reader/Writer interfaces,
// so swapping the library only requires replacing this subpackage.
package goproxyproto

import (
	"bufio"
	"errors"
	"net"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

type reader struct{}

// Read delegates to gop.Read, which itself does a Peek(1) fast-path so that
// non-PROXY (direct) traffic is not blocked waiting for header-sized peeks.
//   - gop.ErrNoProxyProtocol (no signature, or partial signature + EOF) maps to
//     SourceDirect with no bytes consumed (Peek does not advance the reader).
//   - A read deadline during detection (partial candidate prefix with no more
//     bytes) is ambiguous, not malformed: treat as direct. Peek does not
//     consume, so the bytes remain buffered for the subsequent pipe.
//   - Any other library error (signature matched but parse failed, or an I/O
//     error) is surfaced as-is so the gateway can close the malformed connection.
//   - A parsed *Header is mapped to our Header via TCPAddrs(); non-TCP families
//     are out of scope and reported as an error.
func (reader) Read(br *bufio.Reader) (pp.Header, pp.Source, error) {
	h, err := gop.Read(br)
	if err != nil {
		if errors.Is(err, gop.ErrNoProxyProtocol) {
			return pp.Header{}, pp.SourceDirect, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return pp.Header{}, pp.SourceDirect, nil
		}
		return pp.Header{}, 0, err
	}
	src, dst, ok := h.TCPAddrs()
	if !ok {
		return pp.Header{}, 0, errors.New("goproxyproto: non-TCP PROXY header not supported")
	}
	out := pp.Header{SrcPort: uint16(src.Port), DstPort: uint16(dst.Port)}
	if v4 := src.IP.To4(); v4 != nil {
		out.SrcIP, out.DstIP = v4, dst.IP.To4()
		out.Family = pp.FamilyTCP4
	} else {
		out.SrcIP, out.DstIP = src.IP, dst.IP
		out.Family = pp.FamilyTCP6
	}
	if h.Version == 2 {
		return out, pp.SourceV2, nil
	}
	return out, pp.SourceV1, nil
}

// NewReader returns a pp.Reader backed by go-proxyproto.
func NewReader() pp.Reader { return reader{} }
