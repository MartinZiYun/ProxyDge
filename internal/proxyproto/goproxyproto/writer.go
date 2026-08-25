package goproxyproto

import (
	"io"
	"net"

	gop "github.com/pires/go-proxyproto"
	pp "proxydge/internal/proxyproto"
)

// writer emits PROXY Protocol headers in one fixed wire version, chosen at
// construction from tcp.header-version ("v1" → 1, "v2" → 2).
type writer struct {
	version byte
}

// WriteTo emits a PROXY Protocol header for hdr in the configured version,
// via the go-proxyproto library.
//
// Two header shapes are handled:
//
//   - hdr.Family == FamilyUnspec ("address unknown"): emit the protocol's
//     honest no-address form instead of inventing addresses —
//
//     v1: the short "PROXY UNKNOWN\r\n" line;
//     v2: a LOCAL + AF_UNSPEC frame (sig + ver/cmd=0x20 + fam/proto=0x00 +
//     length=0x0000), which tells downstream "this connection carries no
//     trustworthy address information, use your own view".
//
//     This is the family-mismatch=unknown landing zone: the gateway rewrites
//     headers whose declared family contradicts their addresses into the zero
//     Header, and they surface here as UNKNOWN/LOCAL. Per the PROXY protocol
//     spec, downstream MUST implement fallback logic for these forms.
//
//   - any other family: HeaderProxyFromAddrs infers the transport from BOTH
//     addresses (go-proxyproto v0.15.0+ both-ends family selection; v0.7.0
//     used the source alone) and encodes them under it. For
//     family-consistent headers — the only kind reject/unknown paths ever
//     forward, and the only kind direct sockets can produce — this is exact.
//
//     For mismatched headers the library coerces silently (the TCPv6 branches
//     map an IPv4 end through To16() into ::ffff:-mapped form). That behavior
//     is deliberately preserved here, unguarded: it IS what family-mismatch=
//     legacy promises. The v2 wire stays byte-identical across library
//     versions; the v1 text now serializes the mapped form as "::ffff:x.x.x.x"
//     (v0.15.0's netip-based formatter) instead of v0.7.0's collapsed
//     "x.x.x.x". The gateway's FamilyMatchesAddrs check upstream of this call
//     is what keeps reject/unknown paths from ever feeding it mixed-family
//     headers. Never "improve" the coercion here — silently rewriting
//     addresses is precisely the deception this feature exists to make
//     explicit.
func (wr writer) WriteTo(w io.Writer, hdr pp.Header) error {
	if hdr.Family == pp.FamilyUnspec {
		var h *gop.Header
		if wr.version == 1 {
			// TransportProtocol stays UNSPEC → formatVersion1 takes its
			// default branch and emits the short "PROXY UNKNOWN" line.
			h = &gop.Header{Version: 1}
		} else {
			// Command LOCAL + UNSPEC transport: downstream ignores the
			// (absent) address section entirely.
			h = &gop.Header{Version: 2, Command: gop.LOCAL}
		}
		_, err := h.WriteTo(w)
		return err
	}
	src := &net.TCPAddr{IP: hdr.SrcIP, Port: int(hdr.SrcPort)}
	dst := &net.TCPAddr{IP: hdr.DstIP, Port: int(hdr.DstPort)}
	if _, err := gop.HeaderProxyFromAddrs(wr.version, src, dst).WriteTo(w); err != nil {
		return err
	}
	return nil
}

// NewWriter returns a pp.Writer that emits PROXY Protocol headers in the
// given version (1 = text "PROXY TCP4 ...", 2 = binary). config.Validate
// already constrains tcp.header-version to "v1"|"v2"; main maps it to this
// byte, so no defensive clamping here — the library would clamp invalid
// versions to 2 anyway.
func NewWriter(version byte) pp.Writer { return writer{version: version} }
