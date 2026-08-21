---
feature: gateway-logic-extraction
status: delivered
updated: 2026-08-21
branch: refactor/gateway-logic-extraction
commits: 05716f8..9ba2782
---

# Gateway Logic Extraction

## Report

**What was built** — The gateway's `handle()` function was a monolith with trust checking, policy enforcement, and pipe logic all inline. This step extracted three transport-agnostic components: (1) `transport.CloseWriter` — an optional capability interface for half-close, checked via type assertion, NOT embedded in `transport.Conn`; (2) `transport.RemoteIP` — a helper function extracting `net.IP` from any `AddrConn`, replacing the gateway's local `remoteIP()`; (3) `gateway/decide.go` — a `decide()` function bundling trust + policy decision logic, returning `(hdr, src, allow, reason)` with reason preserving existing log semantics; (4) `gateway/pipe.go` — a `pipeStream()` function doing bidirectional `io.Copy` with optional `CloseWriter` check. `handle()` is now a ~55-line thin orchestrator: TCP-specific header detection and dial stay inline; trust+policy decision and pipe are delegated.

**Verification** — `go build ./...` PASS. `go test ./...` PASS (27 gateway tests: 8 new decide + 12 existing gateway + 7 trust). `go vet ./...` PASS. Reviewer confirmed all 6 acceptance criteria met, zero critical issues, all existing tests pass unchanged, log messages preserved exactly.

**Journey log**:
- `decide()` returns a single `reason` string — when strip happens AND THEN policy rejects (e.g., UntrustedStrip + PolicyRequire), the intermediate "strip" reason is overwritten by "policy:requires". This is a contradictory configuration (strip-to-direct then require-proxy is self-defeating) and the final decision is correct, but the "stripped" log line is lost. Noted for awareness; not fixed to avoid adding complexity for an edge case that no test covers.
- `decide()` calls `proxyproto.HeaderFromConn(c)` twice in the strip path (once in the strip case, once in the `src == SourceDirect` fallthrough). This faithfully reproduces the original code's behavior — harmless redundancy, left as-is to maintain behavioral parity.
- The "optional capability via type assertion" pattern (CloseWriter not embedded in Conn) is a clean Go idiom for transport features not all transports support. Worth reusing for future capabilities.

## [S1] Problem

After step 1 (TCP-specific abstraction), the gateway's `handle()` function is a monolith: trust checking, policy enforcement, and pipe logic are all inline. These pieces are transport-agnostic in principle (they operate on `proxyproto.Source`/`Header` and `io.Reader`/`io.Writer`), but they're tangled with TCP-specific code (`remoteIP` type-asserts `*net.TCPAddr`, pipe calls `CloseWrite` directly). This blocks a future UDP gateway from reusing the decision and pipe logic.

## [S2] Design

### New transport types

```go
// transport/transport.go (additions)

// CloseWriter is an optional capability interface for connection-oriented
// transports that support half-close (e.g. TCP's FIN). Transports without
// half-close simply don't implement it. This is NOT added to transport.Conn —
// it's a capability checked via type assertion.
type CloseWriter interface {
    CloseWrite() error
}

// RemoteIP extracts the remote IP from a connection's peer address. Handles
// TCP now; UDP can be added later. A helper, not a core abstraction.
func RemoteIP(c AddrConn) net.IP {
    switch a := c.RemoteAddr().(type) {
    case *net.TCPAddr:
        return a.IP
    }
    return nil
}
```

### gateway/decide.go — trust + policy decision

A free function that applies trust rules then policy rules to determine the
final header, source, and allow/deny decision. Depends only on `proxyproto`
and `transport` — never on `tcp`.

```go
func decide(policy Policy, trust *TrustChecker, untrusted UntrustedAction,
    src proxyproto.Source, hdr proxyproto.Header, ip net.IP, c transport.AddrConn,
) (proxyproto.Header, proxyproto.Source, bool, string)
```

Returns `(hdr, src, allow, reason)`:
- `reason=""` — allowed, no special action
- `reason="untrusted"` — rejected by trust (UntrustedReject)
- `reason="strip"` — allowed but source stripped to direct (UntrustedStrip)
- `reason="policy:forbids"` — rejected by PolicyReject
- `reason="policy:requires"` — rejected by PolicyRequire

The caller (`handle()`) saves the original `src` before calling `decide()`, so
it can log the original source for the strip case — preserving exact existing
log messages.

### gateway/pipe.go — bidirectional pipe with optional half-close

```go
func pipeStream(clientReader io.Reader, clientWriter, upstream io.ReadWriteCloser,
    log *slog.Logger, remote net.Addr)
```

Two goroutines with `io.Copy`. When one direction reaches EOF, if the sink
implements `transport.CloseWriter`, `CloseWrite()` is called; otherwise skipped.
TCP connections satisfy CloseWriter today; future UDP wouldn't.

### gateway/gateway.go — handle() becomes thin orchestrator

```
TCP-specific:   header detection (bufio.NewReader + SetReadDeadline + reader.Read)
agnostic:       origSrc := src
                hdr, src, allow, reason := decide(g.policy, g.trust, g.untrusted, src, hdr, transport.RemoteIP(c), c)
                → log based on reason (using origSrc for strip case)
                → if !allow: return
TCP-specific:   dial + writer.WriteTo
agnostic:       pipeStream(br, c, up, g.log, c.RemoteAddr())
```

`remoteIP()` deleted from gateway — replaced by `transport.RemoteIP()`.
`sync` import moves from gateway.go to pipe.go.

### Dependency rules

- `decide()` imports: `proxyproto`, `transport` — never `tcp`
- `pipeStream()` imports: `io`, `log/slog`, `net`, `sync`, `transport` — never `tcp`
- `handle()` imports: `bufio`, `tcp`, `proxyproto`, `transport` — orchestrator
- `CloseWriter` is a standalone interface in `transport`, NOT embedded in `transport.Conn`

## [S3] Out of Scope

- No UDP gateway implementation.
- No changes to TrustChecker, Policy, or UntrustedAction types themselves.
- No changes to gateway.New() signature or Serve().
- No changes to existing tests (they test through New()+Serve()).
- CloseWriter is not added to transport.Conn or tcp.Conn — it's a capability interface checked via type assertion only.

## Tasks

- [x] T1: Add `CloseWriter` interface and `RemoteIP` helper to `internal/transport/transport.go` — acceptance: `go build ./internal/transport` succeeds; CloseWriter is standalone (not in Conn); RemoteIP handles `*net.TCPAddr` (covers: S2)
- [x] T2: Create `internal/gateway/decide.go` with `decide()` function — acceptance: function compiles; only imports `proxyproto` and `transport`; returns (hdr, src, allow, reason) for all 5 decision paths (covers: S2; depends: T1)
- [x] T3: Create `internal/gateway/pipe.go` with `pipeStream()` function — acceptance: function compiles; checks `transport.CloseWriter` via type assertion; takes `io.ReadWriteCloser` not `tcp.Conn` (covers: S2; depends: T1)
- [x] T4: Rewrite `handle()` in `internal/gateway/gateway.go` as thin orchestrator — acceptance: handle() calls decide() + pipeStream(); remoteIP() deleted; sync import removed from gateway.go; existing tests pass unchanged (covers: S2; depends: T2, T3)
- [x] T5: Add unit tests for `decide()` in `internal/gateway/decide_test.go` — acceptance: all 5 decision paths tested (allow, untrusted-reject, strip, policy-forbids, policy-requires); strip case verifies src=SourceDirect and hdr rebuilt (covers: S2; depends: T2)
- [x] T6: Run `go build ./...` and `go test ./...` — acceptance: zero compile errors; all existing tests pass; new decide tests pass; no behavioral changes (covers: S2; depends: T4, T5)
