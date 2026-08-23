package proxyproto

// FamilyMatchesAddrs reports whether hdr's declared address family matches
// what its addresses actually express — checked PER ADDRESS against the
// declared family, never by comparing src against dst. A header passes only
// when each address individually satisfies the family the header claims.
//
// Contract (nil-safe; callers need no preconditions):
//
//	declared TCP4  consistent ⇔ SrcIP != nil && DstIP != nil
//	                             && SrcIP.To4() != nil && DstIP.To4() != nil
//	declared TCP6  consistent ⇔ SrcIP != nil && DstIP != nil
//	                             && SrcIP.To4() == nil && DstIP.To4() == nil
//	                             && SrcIP.To16() != nil && DstIP.To16() != nil
//	declared Unspec             ⇒ true (claims nothing; nothing to contradict)
//	anything else               ⇒ false (UDP families never reach the TCP path)
//
// Why nil is spelled out instead of relying on the To4() clauses alone:
// net.IP(nil).To4() == nil, so a TCP6 header carrying two nil IPs would
// vacuously satisfy "both sides not convertible to IPv4". Those nils are
// exactly what the stream reader produces for crafted wire input such as
// "AF_INET6 source + destination that isn't valid IPv6" (the reader
// normalizes the destination through To4() and stores nil) — precisely the
// malformed shape this predicate exists to catch. They must FAIL, not pass.
// The To16() != nil clauses guard symmetrically against zero-length non-nil
// IPs.
//
// Typical violation shapes (see docs/compose/spec/tcp-header-version.md):
//   - declared INET6 with an ::ffff:-mapped IPv4 destination — the mapped
//     form converts via To4(), so it fails the TCP6 branch even though both
//     addresses are 16 bytes on the wire;
//   - declared TCP4 with a nil destination (the reader-normalized case above).
//
// Direct sockets can never violate this: an established TCP connection's
// remote and local addresses always share one family, and HeaderFromAddrs
// derives both sides from the same pair.
func FamilyMatchesAddrs(h Header) bool {
	switch h.Family {
	case FamilyTCP4:
		return h.SrcIP != nil && h.DstIP != nil &&
			h.SrcIP.To4() != nil && h.DstIP.To4() != nil
	case FamilyTCP6:
		return h.SrcIP != nil && h.DstIP != nil &&
			h.SrcIP.To4() == nil && h.DstIP.To4() == nil &&
			h.SrcIP.To16() != nil && h.DstIP.To16() != nil
	case FamilyUnspec:
		return true
	default:
		return false
	}
}
