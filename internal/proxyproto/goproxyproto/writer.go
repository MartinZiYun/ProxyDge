package goproxyproto

import (
	"io"
	"net"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

type writer struct{}

// WriteTo emits a PROXY Protocol v2 header for hdr via the library.
// HeaderProxyFromAddrs infers the TCPv4/TCPv6 transport from the addresses and
// sets Command=PROXY when the addresses are usable.
func (writer) WriteTo(w io.Writer, hdr pp.Header) error {
	src := &net.TCPAddr{IP: hdr.SrcIP, Port: int(hdr.SrcPort)}
	dst := &net.TCPAddr{IP: hdr.DstIP, Port: int(hdr.DstPort)}
	if _, err := gop.HeaderProxyFromAddrs(2, src, dst).WriteTo(w); err != nil {
		return err
	}
	return nil
}

// NewWriter returns a pp.Writer that always emits PROXY Protocol v2.
func NewWriter() pp.Writer { return writer{} }
