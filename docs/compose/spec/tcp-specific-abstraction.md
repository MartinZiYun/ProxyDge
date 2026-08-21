---
feature: tcp-specific-abstraction
status: designed
updated: 2026-08-21
branch: refactor/tcp-protocol-abstraction
commits: TBD
---

# TCP-Specific Transport Abstraction

## Report

## [S1] Problem

The `internal/transport` package claims to be a transport-agnostic abstraction but is actually TCP-specific: `Conn` includes `CloseWrite()` (TCP FIN / half-close) and `SetReadDeadline()` (TCP stream detection). `proxyproto` defines a duplicate `AddrConn` interface solely to avoid an import cycle with `transport`. The gateway hardcodes TCP semantics without acknowledging it. This mislabeling blocks a future multi-protocol architecture (TCP/UDP gateways) because there is no clean boundary between cross-transport concepts and TCP-specific behavior.

## [S2] Design

### Three-layer package structure

```
internal/protocol/    ← enum, config/routing only, no transport behavior
internal/transport/   ← cross-transport base (interfaces only, no implementation)
internal/tcp/         ← TCP-specific implementation
```

### internal/protocol

A lightweight enum for routing and configuration. It carries no behavior — not a transport interface, not a factory selector, just a string type and constants. Only TCP and UDP are defined; QUIC and UNIX are not added until the product actually plans them.

```go
package protocol

type Protocol string

const (
    TCP Protocol = "tcp"
    UDP Protocol = "udp"  // reserved; no implementation yet
)
```

### internal/transport (rewritten)

Cross-transport interfaces only. No concrete implementations, no TCP-specific methods. This is the concept layer that future connection-oriented transports will satisfy.

```go
package transport

// AddrConn provides address access without I/O. Separated from Conn so
// proxyproto can build headers from any address-bearing connection without
// importing a concrete transport.
type AddrConn interface {
    LocalAddr() net.Addr
    RemoteAddr() net.Addr
}

// Conn is the minimal I/O connection abstraction shared by
// connection-oriented transports. Transport-specific semantics
// remain in transport-specific packages.
type Conn interface {
    AddrConn
    Read([]byte) (int, error)
    Write([]byte) (int, error)
    Close() error
}
```

`AddrConn` is embedded in `Conn` so every `Conn` satisfies `AddrConn`. Note: `Conn` is not a claim that all transports (including datagram-oriented ones like UDP) can be unified into this interface — UDP's `ReadFrom`/`WriteTo` model does not naturally fit `Read([]byte)`/`Write([]byte)`. A future UDP transport may define its own connection abstraction rather than satisfying `transport.Conn`.

### internal/tcp (new; replaces old internal/transport content)

TCP-specific types. `Conn` embeds `transport.Conn` and adds TCP-only methods. All concrete adapters (`tcpListener`, `TCPDialer`, `Listen()`) move here from the old `transport` package.

```go
package tcp

// Conn is a TCP byte-stream connection with half-close and read-deadline
// control. *net.TCPConn satisfies it directly.
type Conn interface {
    transport.Conn
    CloseWrite() error
    SetReadDeadline(time.Time) error
}

// Listener accepts inbound tcp.Conn connections.
type Listener interface {
    Accept() (Conn, error)
    Close() error
    Addr() net.Addr
}

// Dialer dials outbound tcp.Conn connections.
type Dialer interface {
    Dial(network, address string) (Conn, error)
}

// TCPDialer is the production TCP dialer.
type TCPDialer struct{}
func (TCPDialer) Dial(network, address string) (Conn, error) { ... }

// Listen creates a TCP Listener.
func Listen(network, address string) (Listener, error) { ... }
```

### internal/proxyproto (updated)

Removes local `AddrConn`; imports `transport` for `transport.AddrConn`. `HeaderFromConn` signature changes from `AddrConn` to `transport.AddrConn`. The type-assertion to `*net.TCPAddr` inside `HeaderFromConn` stays — it is the TCP-specific address extraction, acceptable while only TCP exists.

### internal/gateway (updated)

Type references change from `transport.*` to `tcp.*`:
- `Gateway` struct: `ln tcp.Listener`, `dialer tcp.Dialer`
- `New()` params: `ln tcp.Listener, dialer tcp.Dialer`
- `handle(c tcp.Conn)`
- `remoteIP(c tcp.Conn)` — still type-asserts `*net.TCPAddr`, stays unexported in gateway

No logic extraction. Policy, trust, and pipe logic remain unchanged in structure.

### main.go (updated)

- `transport.Listen("tcp", ...)` → `tcp.Listen("tcp", ...)`
- `transport.TCPDialer{}` → `tcp.TCPDialer{}`

### Dependency graph (acyclic)

```
protocol      (standalone, no imports)
transport     (imports net only)
  ↑ tcp       (imports transport, net)
  ↑ proxyproto (imports transport, net)
gateway       (imports tcp, proxyproto)
goproxyproto  (imports proxyproto, go-proxyproto lib)
main          (imports tcp, gateway, config, goproxyproto, ...)
```

### Naming: TCPDialer in tcp package

`tcp.TCPDialer` is slightly redundant (TCP prefix in tcp package) but mirrors Go stdlib convention (`net.TCPConn`, `net.TCPAddr`, `net.TCPListener` — all in package `net` with TCP prefix). Keeping the name minimizes the diff and preserves clarity.

## [S3] Out of Scope

- No UDP implementation. The `protocol.UDP` constant is reserved but unused.
- No QUIC or UNIX constants added to the `protocol` enum. Only TCP and UDP.
- No `Protocol` field added to Config struct, YAML, env, or flags. Type exists only; config wiring deferred until a second transport is implemented.
- No logic extraction from gateway. Policy, trust, pipe, and `remoteIP` stay in gateway with unchanged structure.
- No `transport.RemoteIP` helper. `remoteIP` stays as an unexported function in the TCP gateway.
- No changes to proxyproto Reader/Writer interfaces or Header struct shape.
- No changes to goproxyproto adapter logic.
- No changes to config, i18n, version, or main's command dispatch logic.

## Tasks

- [x] T1: Create `internal/protocol/protocol.go` with Protocol type and TCP/UDP constants — acceptance: `go build ./internal/protocol` succeeds; type is standalone with no imports beyond builtin (covers: S2)
- [ ] T2: Rewrite `internal/transport/transport.go` with cross-transport AddrConn + Conn interfaces only — acceptance: package exports AddrConn and Conn; no TCP-specific methods; no concrete implementations (covers: S2)
- [ ] T3: Create `internal/tcp/tcp.go` with TCP-specific Conn/Listener/Dialer/TCPDialer/Listen (content moved from old transport.go, Conn embeds transport.Conn) — acceptance: file compiles; `tcp.Conn` embeds `transport.Conn` and adds CloseWrite/SetReadDeadline (covers: S2; depends: T2)
- [ ] T4: Update `internal/proxyproto/proxyproto.go` to import transport and use `transport.AddrConn` instead of local `AddrConn` — acceptance: `proxyproto.AddrConn` removed; `HeaderFromConn` takes `transport.AddrConn`; package compiles (covers: S2; depends: T2)
- [ ] T5: Update `internal/gateway/gateway.go` and `trust.go` to use `tcp.*` instead of `transport.*` for Listener/Dialer/Conn types — acceptance: Gateway struct and New() signature use tcp.Listener/tcp.Dialer; handle() takes tcp.Conn; remoteIP takes tcp.Conn (covers: S2; depends: T3, T4)
- [ ] T6: Update `main.go` to use `tcp.Listen`/`tcp.TCPDialer` instead of `transport.*` — acceptance: main.go imports tcp not transport; `go build` succeeds (covers: S2; depends: T3, T5)
- [ ] T7: Move `internal/transport/transport_test.go` to `internal/tcp/tcp_test.go` and update references; update `internal/gateway/gateway_test.go` to use `tcp.*` — acceptance: test files compile and use tcp.Listen/tcp.TCPDialer (covers: S2; depends: T3, T5)
- [ ] T8: Run `go build ./...` and `go test ./...` — acceptance: zero compile errors; all existing tests pass with no behavioral changes (covers: S2; depends: T7)
