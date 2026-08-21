// decide.go extracts the trust + policy decision pipeline from handle() so
// the logic is reusable across transports. The function depends only on
// proxyproto and transport types — never on tcp.
package gateway

import (
	"net"

	"proxydge/internal/proxyproto"
	"proxydge/internal/transport"
)

// decide applies trust rules then policy rules to determine the final header,
// source, and allow/deny decision for a connection.
//
//   - reason="" — allowed, no special action
//   - reason="untrusted" — rejected by trust (UntrustedReject)
//   - reason="strip" — allowed but source stripped to direct (UntrustedStrip)
//   - reason="policy:forbids" — rejected by PolicyReject
//   - reason="policy:requires" — rejected by PolicyRequire
//
// The caller should save the original src before calling, so it can log the
// original source for the strip case.
func decide(policy Policy, trust *TrustChecker, untrusted UntrustedAction,
	src proxyproto.Source, hdr proxyproto.Header, ip net.IP, c transport.AddrConn,
) (proxyproto.Header, proxyproto.Source, bool, string) {
	reason := ""

	// Trust check: only trusted networks may send PROXY headers.
	// remoteIP comes from the socket, never from the PROXY header's SrcIP.
	if src != proxyproto.SourceDirect && !trust.IsTrusted(ip) {
		switch untrusted {
		case UntrustedReject:
			return hdr, src, false, "untrusted"
		case UntrustedStrip:
			reason = "strip"
			src = proxyproto.SourceDirect
			hdr = proxyproto.HeaderFromConn(c)
		}
	}

	// Policy check: based on the (possibly normalized) source, not the raw
	// presence of a PROXY header.
	switch {
	case policy == PolicyReject && src != proxyproto.SourceDirect:
		return hdr, src, false, "policy:forbids"
	case policy == PolicyRequire && src == proxyproto.SourceDirect:
		return hdr, src, false, "policy:requires"
	case src == proxyproto.SourceDirect:
		hdr = proxyproto.HeaderFromConn(c)
	}

	return hdr, src, true, reason
}
