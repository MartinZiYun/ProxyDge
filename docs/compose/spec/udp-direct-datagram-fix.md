---
feature: udp-direct-datagram-fix
status: in-progress
updated: 2026-08-25
branch: udp-direct-datagram-fix
commits: TBD
---

# UDP Direct-Datagram PROXY Header Fix

## Report

## [S1] Problem

In UDP mode with `policy=use` and `udp.header-mode=every_datagram`, every
datagram from a **direct client** (no PROXY header) is dropped with:

```
level=ERROR msg="format datagram" remote=192.x.x.x:60673 err="goproxyproto: format PROXY header: proxyproto: invalid address"
```

The synthesized PROXY header cannot be formatted, so `FormatDatagram` returns
an error and the datagram is never forwarded. Direct clients are completely
broken under this configuration.

## [S2] Design

### Root cause

For a direct datagram, `Decide` synthesizes the header from the real socket
addresses via `proxyproto.HeaderFromConn` → `HeaderFromAddrs(RemoteAddr,
LocalAddr)`. The destination comes from `listener.LocalAddr()`. On a
**wildcard/dual-stack listener** (e.g. `:port`), `LocalAddr().IP` is the IPv6
unspecified address `::` (16 zero bytes).

`buildAddrHeader` selects the family from the source alone: when the source is
IPv4 it takes the v4 branch and sets `h.DstIP = dstIP.To4()`. For `::`,
`net.IP{0×16}.To4()` returns **nil** — so the synthesized `Header` carries a
nil `DstIP` while `Family == FamilyUDP4`, an internally inconsistent value.

That nil propagates to `goproxyproto.FormatDatagram` → the library's
`Header.Format()` → `formatVersion2`, where `addrDst = destIP.To4()` is nil and
the library returns `ErrInvalidAddress` (`proxyproto: invalid address`),
dropping the datagram.

### Why tests miss it

Existing UDP gateway tests bind to `127.0.0.1:0` and `[::1]:0` — specific
addresses whose `LocalAddr().IP` is real (`127.0.0.1` / `::1`), so `To4()`/`To16()`
never returns nil. The bug only manifests on a wildcard bind, which no test
exercises.

### Fix

In `buildAddrHeader`, when the source is IPv4 but the destination does not
have a valid `To4()` (the wildcard/mismatch case), use `net.IPv4zero`
(`0.0.0.0`, the source-family unspecified address) instead of nil. The
resulting header is consistent: `FamilyUDP4`, valid non-nil `SrcIP` (the real
client) and a valid `DstIP` (`0.0.0.0`). `FormatDatagram` then emits a valid
PROXY v2 UDP4 frame carrying the real client source endpoint — which is the
information that matters for `policy=use`.

This is the honest representation for a wildcard listener: the per-datagram
destination IP is genuinely unknown without socket ancillary data, while the
source is fully accurate. TCP is unaffected (an accepted connection's
`LocalAddr` is always a specific, family-consistent address).

### Dependency upgrade

`github.com/pires/go-proxyproto` is pinned at `v0.7.0`; upgrade to `v0.15.0`.

Relevant v0.15.0 change: `HeaderProxyFromAddrs` now selects the transport
family from **both** addresses (v0.7.0 used the source alone). With a nil
destination IP, v0.15.0 falls through to `UNSPEC`/`LOCAL` — a header with
**no addresses** — so the upgrade alone stops the error but still defeats
`policy=use` (no client source is conveyed). The nil-IP fix above is therefore
required regardless of the upgrade.

The adapter (`internal/proxyproto/goproxyproto`) uses only stable library APIs
(`Read`, `ErrNoProxyProtocol`, `Header.TCPAddrs`, `HeaderProxyFromAddrs`,
`Header.Format`/`WriteTo`, `LOCAL`) whose signatures are unchanged in v0.15.0;
the hand-rolled datagram parser is untouched. For TCP, the gateway guarantees
family-consistent headers upstream of `WriteTo`, so v0.15.0's stricter
both-ends selection is behaviorally equivalent for the reject/unknown paths.

### v0.15.0 family-mismatch reconciliation (discovered during upgrade)

v0.15.0's both-ends family selection and netip-based v1 formatter change the
behavior for **mixed-family** TCP headers (one v4 end, one v6 end), which only
the `family-mismatch=legacy` path forwards to the writer:

- v0.7.0 picked the family from the source alone, so a v4-src/v6-dst pair was
  labeled TCPv4 and the library returned `ErrInvalidAddress` (`dst.To4()` nil).
  v0.15.0 coerces it to **TCPv6** (v4 end → `::ffff:`-mapped) with no error.
- v0.15.0's v1 formatter serializes a `::ffff:`-mapped address as
  `::ffff:1.2.3.4` (netip preserves the mapped form); v0.7.0 collapsed it to
  `1.2.3.4` via `net.IP.String()`. The **v2** wire is unaffected (both versions
  emit the 16-byte mapped form).

Two tests encoded v0.7.0-specific behavior and were reconciled to v0.15.0's
(more spec-correct) behavior: `TestWriterMixedFamilyErrorsBothVersions` →
`TestWriterMixedFamilyCoercesToV6` (asserts coercion, not error); the
`TestLegacyV1GoldenText` v1 golden updated to `::ffff:192.168.1.1`. The
now-stale "source address alone" / "byte-identical" claims in `writer.go` and
`gateway.go` comments were corrected. The `family-mismatch=reject`/`unknown`
production paths are unaffected (their `FamilyMatchesAddrs` guard, our code,
catches mixed-family before the writer).

## [S3] Out of Scope

- Retrieving the real per-datagram destination IP via socket PKTINFO control
  messages (`IPV{4,6}_RECVPKTINFO` + `ReadMsgUDP`). Correct but adds platform
  complexity and a `golang.org/x/net` runtime dependency; not needed to fix the
  reported drop. The `0.0.0.0`/`::` destination is acceptable for wildcard
  listeners.
- Refactoring the datagram adapter to use v0.15.0's native
  `ParseUDPDatagram`/`FormatUDPDatagram`. The hand-rolled parser is correct and
  unaffected; replacing it is unrelated cleanup.

## Tasks

- [x] T1: Upgrade `github.com/pires/go-proxyproto` v0.7.0 → v0.15.0 and
  reconcile the v0.7.0-specific family-mismatch tests/comments — acceptance:
  `go build ./...` + `go vet ./...` + `go test ./...` all pass on v0.15.0;
  `go.mod` pins v0.15.0; `TestWriterMixedFamilyCoercesToV6` and the updated
  `TestLegacyV1GoldenText` reflect v0.15.0 behavior. (covers: S2)
- [x] T2: Fix nil-IP family mismatch in `buildAddrHeader`
  (`internal/proxyproto/proxyproto.go`) — acceptance: a v4-source header built
  from an IPv6-unspecified destination has a non-nil `DstIP` equal to
  `net.IPv4zero`, not nil. (covers: S2)
- [x] T3: Regression test in `internal/proxyproto/goproxyproto/datagram_test.go`
  — acceptance: `FormatDatagram` of a header built via `HeaderFromAddrs` from a
  v4 source + `::` destination round-trips through `ParseDatagram` carrying the
  real client source IP:port and a `0.0.0.0` destination; fails without T2.
  (covers: S2; depends: T1, T2)
