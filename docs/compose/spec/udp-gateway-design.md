---
feature: udp-gateway-design
status: designed
updated: 2026-08-21
branch: TBD
commits: TBD
---

# UDP Gateway Design (Revised)

## Report

## [S1] Problem

ProxyDge is a PROXY Protocol compatibility/normalization layer. It currently supports only TCP. A UDP gateway is needed, but UDP has fundamentally different semantics — message boundaries, no connection lifecycle, no half-close — and must NOT be adapted into the existing `pipeStream()` / `io.ReadWriteCloser` / `transport.CloseWriter` abstraction. UDP needs its own datagram/session model.

The core design principle is: **input format ≠ output format**. ProxyDge normalizes incoming datagrams (which may carry PROXY headers in various patterns) into a normalized internal representation, then encodes them in a configurable output format. Input and output modes are fully decoupled.

## [S2] Design

### Architectural constraint

```
TCP gateway                          UDP gateway
─────────────                        ─────────────
tcp.Conn (stream + half-close)       UDPSession (datagram + idle timeout)
pipeStream() (io.Copy + CloseWrite)  datagram forwarding (per-message)
bufio.Reader (PROXY header peek)    DatagramReader.ParseDatagram([]byte)
proxyproto.Writer (stream WriteTo)   DatagramWriter.FormatDatagram(Hdr, payload)
transport.CloseWriter (FIN)          NOT USED
```

Shared logic only:
- `gateway.decide()` — trust + policy decision
- `gateway.Policy`, `TrustChecker`, `UntrustedAction` — config enums
- `proxyproto.Header`, `Source` — PROXY protocol types (extended with UDP families)
- `transport.RemoteIP` — IP extraction helper (extended for `*net.UDPAddr`)

NOT shared (TCP-only, not imported by UDP):
- `pipeStream()`, `transport.CloseWriter`, `tcp.Conn`

### Final pipeline architecture

```
UDP socket
    │
    ▼
ReadFromUDP()  →  actualPeer (real source endpoint)
    │
    ▼
UDP input decoder (DatagramReader)
    │  auto-detect: direct / first_datagram / every_datagram / malformed
    │
    ▼
normalized datagram
    ├── Payload      []byte             (stripped of PROXY header)
    ├── Source       proxyproto.Source   (Direct | V1 | V2)
    ├── Header       proxyproto.Header   (normalized src/dst addresses)
    └── ActualPeer   net.Addr            (real socket source — for trust)
    │
    ▼
session lookup (existing?) → recover input flow state if first_datagram input
    │
    ▼
decide()  ←  trust (ActualPeer) + policy (shared with TCP)
    │
    ├── rejected → DROP (no session created, no DialUDP, no goroutine)
    │
    ▼  (allowed)
    │
    ├── new source → create UDPSession / DialUDP / start reader goroutine
    │                 persist inputSource (AFTER trust check passed)
    │
    ▼
UDP output encoder (DatagramWriter)
    ├── every_datagram (default): [PROXY][payload] per datagram
    └── first_datagram (compat):  [PROXY][payload] then [payload]...
    │
    ▼
connected UDP socket (per-session)
    │
    ▼
upstream
```

Core principles:
```
1. Input format ≠ Output format
2. decide() BEFORE session creation — rejected sources consume zero resources
3. Persisted session metadata must own its address byte slices (deep-copy)
4. inputSource persisted only AFTER trust+policy passes — untrusted metadata
   never enters session state
5. Protocol normalization is stateless where possible;
   session state only when compatibility mode requires it
```

### Security invariants (summary)

All four invariants are enforced in the pipeline and tested in [D16]:

1. **Resource ordering** (D8 step 5–7): `decide()` runs BEFORE `DialUDP`/session creation. Rejected sources consume zero resources (no session, no fd, no goroutine).
2. **Header ownership** (D9): `inputSource` Header is deep-copied (`cloneHeader`). No references to the reusable `ReadFromUDP` packet buffer persist across datagrams.
3. **inputSource trust gating** (D9): `inputSource` is persisted ONLY after `decide()` returns allow. Untrusted PROXY metadata never enters session state.
4. **No state inheritance on recreation** (D10/D14): `expire()` clears `inputSource` + removes from map atomically (`sync.Once`). New session for same key starts with nil state. Prevents source port reuse attacks.

### Package structure

```
internal/
  udp/
    gateway.go     — UDPGateway: New(), Serve(), handleDatagram()
    session.go     — UDPSession: lifecycle, reader goroutine, idle timer
    manager.go     — UDPSessionManager: session map, max sessions, cleanup
    forward.go     — datagram forwarding: input decode → normalized → decide → output encode
  transport/
    transport.go   — RemoteIP extended: *net.UDPAddr case added
  proxyproto/
    proxyproto.go  — FamilyUDP4/UDP6; HeaderFromAddrs(src, dst); DatagramReader/Writer interfaces
    goproxyproto/  — datagram adapter: ParseDatagram/FormatDatagram using go-proxyproto UDP API
  gateway/
    decide.go      — shared (no changes)
    pipe.go        — TCP-only (no changes, NOT imported by udp)
    trust.go       — shared (no changes)
    gateway.go     — Policy/UntrustedAction types (no changes)
```

Dependency graph (acyclic):
```
gateway    ← udp (for decide, Policy, TrustChecker)
proxyproto ← udp (for Header, Source, DatagramReader/Writer)
transport  ← udp (for RemoteIP)
tcp        (NOT imported by udp)
```

### [D1] Input/output mode separation

**ProxyDge's core responsibility**: normalize incoming PROXY protocol datagrams into a configurable output format. Input and output modes are fully decoupled.

**Input modes** (auto-detected, not configured):
```
direct          — no PROXY header, payload only
first_datagram  — [PROXY][P1], then [P2], [P3]...  (header on first datagram only)
every_datagram  — [PROXY][P1], [PROXY][P2], [PROXY][P3]...  (header on every datagram)
```

The input decoder auto-detects which mode is in use by inspecting each datagram:
- If datagram starts with PROXY v2 signature → parse header + payload.
- If datagram has no signature → direct/headerless payload; check session flow state (see [D9]).
- If datagram starts with signature but parse fails → **malformed, reject/drop. Never fallback to payload.**

**Output modes** (configurable):
```
every_datagram (default)  — [PROXY][P1], [PROXY][P2]...  each datagram self-describing
first_datagram (compat)   — [PROXY][P1], then [P2]...    requires downstream flow state
```

Config concept:
```yaml
proxy_protocol:
  udp_input: auto          # always auto-detect (direct/first/every)
  udp_output: every_datagram  # default; first_datagram for compat
```

**`headerSent` is NOT a protocol model concept.** It is an implementation detail of `first_datagram` output mode only. For `every_datagram` output, each datagram is independently encoded — the session carries no protocol state.

### [D2] NormalizedDatagram representation

All input parsing produces a `NormalizedDatagram` before any decision or forwarding. This eliminates scattered "has header?" branches:

```go
type NormalizedDatagram struct {
    Payload    []byte              // application data, PROXY header stripped
    Source     proxyproto.Source   // Direct | V1 | V2
    Header     proxyproto.Header   // normalized src/dst addresses
    ActualPeer net.Addr            // real socket source (for trust decisions)
}
```

Key distinction:
- **`ActualPeer`**: the real UDP socket source endpoint from `ReadFromUDP()`. Used for trust checking. Never trusted from PROXY header.
- **`Source`**: the final source to propagate downstream. Comes from PROXY header (if trusted) or from `ActualPeer` (if direct/untrusted).
- **`Header`**: the normalized address information. For direct: built from `ActualPeer` + listener address. For PROXY: from the parsed header (if trusted).

Pipeline:
```
input parsing → NormalizedDatagram → decide() → output encoding → upstream
```

TCP and UDP share the "normalized protocol information / decision" concept, not stream APIs.

### [D3] Datagram-level reader/writer abstractions

UDP must NOT reuse the TCP stream-oriented `proxyproto.Reader` (`Read(*bufio.Reader)`) or `proxyproto.Writer` (`WriteTo(io.Writer, Header)`). New datagram-level interfaces:

```go
// proxyproto/proxyproto.go

// DatagramReader parses a UDP datagram, detecting and extracting a PROXY v2
// header if present.
//
// - datagram has PROXY v2 signature + valid header → (Header, payload, V2, nil)
// - datagram has no signature → (zero Header, datagram, Direct, nil)
// - datagram has signature but malformed → (zero, nil, 0, err) — caller MUST drop
type DatagramReader interface {
    ParseDatagram(data []byte) (hdr Header, payload []byte, src Source, err error)
}

// DatagramWriter encodes a PROXY v2 header + payload into a complete datagram.
type DatagramWriter interface {
    FormatDatagram(hdr Header, payload []byte) ([]byte, error)
}
```

Implementation in `goproxyproto` adapter uses the library's UDP API directly — NOT `bufio.Reader`, NOT `bytes.Reader` faking a stream, NOT `WriteTo(io.Writer)` + manual payload concatenation.

### [D4] HeaderFromAddrs — address-based header construction

TCP currently uses `HeaderFromConn(transport.AddrConn)`. For UDP, the natural inputs are already addresses, not a Conn:

```go
// UDP: clientAddr from ReadFromUDP(), localAddr from listener.LocalAddr()
hdr := proxyproto.HeaderFromAddrs(clientAddr, listener.LocalAddr())

// TCP (can also use this, or keep HeaderFromConn):
hdr := proxyproto.HeaderFromAddrs(c.RemoteAddr(), c.LocalAddr())
```

`HeaderFromAddrs(src, dst net.Addr) Header` type-asserts `*net.TCPAddr` or `*net.UDPAddr` and builds the appropriate family. `HeaderFromConn` can be refactored to delegate to `HeaderFromAddrs` internally. **Do NOT create a fake Conn abstraction just to call `HeaderFromConn` for UDP.**

### [D5] Session lifecycle

Session manages routing, socket lifecycle, and idle timeout — NOT protocol state:

```
                    datagram from new source
                           │
                    ┌──────▼──────┐
                    │    NEW       │  create session, dial upstream,
                    │              │  start reader goroutine
                    └──────┬──────┘
                           │
                    ┌──────▼──────┐
              ┌────│   ACTIVE     │◄──── new datagram refreshes idle timer
              │    │              │      reader goroutine forwards responses
              │    └──────┬──────┘
              │           │ (no activity for idleTimeout)
              │    ┌──────▼──────┐
              │    │   EXPIRED    │  close upstream socket,
              │    │              │  signal reader goroutine to exit,
              │    │              │  clear input flow state (if any),
              │    │              │  remove from session map
              │    └─────────────┘
              │
              │ datagram arrives after expiry
              └──────► NEW session (same source, fresh state — no inheritance)
```

**Session responsibilities**:
- source endpoint (sessionKey)
- upstream connected socket
- lifecycle (idle timer, done channel)
- session limit accounting
- response routing (upstream → client)

**NOT session responsibilities**:
- "save PROXY header once, inherit for subsequent packets" — only `first_datagram` output mode uses `headerSent` as output state
- protocol parsing/encoding — that's the DatagramReader/Writer's job

For `every_datagram` output: each datagram independently encodes PROXY header. Session carries zero protocol state.

For `first_datagram` output: `headerSent atomic.Bool` is output-side state only.

For `first_datagram` input: session stores input-side source mapping (see [D9]).

### [D6] Session key

```go
type sessionKey struct {
    ip   [16]byte  // net.IP.To16() — IPv4-in-IPv6 form
    port uint16
    zone string   // IPv6 zone (e.g., "eth0" for link-local) — "" for IPv4/global
}
```

- **Key = (sourceIP, sourcePort, zone)** from `ReadFromUDP()`.
- IPv6 link-local addresses (`fe80::1%eth0`) have a zone — without it, two clients on different interfaces with the same link-local address would collide.
- **`keyFromUDPAddr(addr *net.UDPAddr) sessionKey`** — unified canonicalization: `ip = addr.IP.To16()`, `port = uint16(addr.Port)`, `zone = addr.Zone`. All key construction goes through this helper — no ad-hoc key creation.
- Single downstream target for all sessions — not part of key.
- `sync.Map` with `LoadOrStore` for atomic create-or-get.

### [D7] Upstream socket strategy: per-session connected UDP socket

**Decision: per-session `net.DialUDP` (connected socket).**

- Connected UDP socket: `Write()` sends to fixed downstream, `Read()` only receives from downstream.
- OS kernel filters responses by source — no application-level routing table.
- Clean isolation per session.
- fd cost bounded by `maxSessions`.
- **Pre-resolved upstream**: `net.ResolveUDPAddr("udp", upstream)` called ONCE in `UDPGateway.New()` or `Serve()` startup. Each session calls `net.DialUDP(network, nil, preResolvedAddr)` — no repeated DNS/resolution per session.

Rejected: shared upstream socket (`ListenUDP` + `WriteToUDP` + routing table — more complex, race-prone).

### Max datagram size

- **Max datagram size**: 65535 bytes (UDP max). Configurable lower limit via `max-datagram-size` if needed.
- **Oversized datagram = drop**: if `ReadFromUDP` returns `n > maxDatagramSize`, drop the datagram. Never truncate and parse a truncated datagram — a truncated PROXY header or payload would produce incorrect normalization.
- The `ReadFromUDP` buffer is sized to `maxDatagramSize + PROXY header overhead` (28 bytes IPv4 / 52 bytes IPv6) to accommodate the largest possible datagram.

### [D8] Bidirectional forwarding model

```
Client datagram                          Upstream response
────────────────                          ─────────────────
     │                                          │
     ▼                                          ▼
┌─────────────────────┐               ┌──────────────────┐
│ UDP Listener (1)    │               │ Session's upstream│
│ ReadFromUDP()       │               │ socket (connected)│
│      │              │               │      │             │
│      ▼              │               │      ▼             │
│ DatagramReader      │               │ Read() (blocks)   │
│ .ParseDatagram()    │               │      │             │
│      │              │               │      ▼             │
│      ▼              │               │ WriteToUDP         │
│ NormalizedDatagram  │               │ (to client)        │
│      │              │               │      │             │
│      ▼              │               │      ▼             │
│ decide()            │               │ refresh idle timer│
│      │              │               │ loop               │
│      ▼              │               └──────────────────┘
│ DatagramWriter      │
│ .FormatDatagram()   │
│      │              │
│      ▼              │
│ session.upstream    │
│ .Write()            │
│ refresh idle timer  │
└─────────────────────┘
```

**Client → Upstream** (listener goroutine) — **security-ordered pipeline**:
1. `ReadFromUDP(buf)` → datagram + `actualPeer *net.UDPAddr`.
2. `DatagramReader.ParseDatagram(datagram)` → `NormalizedDatagram` (or drop on malformed — never fallback).
3. Session lookup by `sessionKey{actualPeer.IP, actualPeer.Port}`.
4. If existing session AND datagram is headerless (Source=Direct): recover `inputSource` from session flow state → set Source/Header.
5. `decide(policy, trust, untrusted, nd.Source, nd.Header, transport.RemoteIP(actualPeer), ...)` → allow/reject.
6. **If rejected: DROP. No session created, no DialUDP, no goroutine, no resource consumed.**
7. If allowed AND new source: create `UDPSession`, `DialUDP`, start reader goroutine.
8. If allowed AND `first_datagram` input AND this datagram had a PROXY header: **persist `inputSource` (deep-copied Header) — ONLY after trust check passed.**
9. `DatagramWriter.FormatDatagram(hdr, nd.Payload)` → encoded datagram (or raw payload for `first_datagram` output after headerSent).
10. `session.upstream.Write(encodedDatagram)`.
11. Refresh idle timer.

**SECURITY INVARIANT — resource ordering**: steps 5–6 (decide + reject) execute BEFORE step 7 (session creation). An untrusted source cannot consume a session slot, file descriptor, or goroutine. This prevents resource exhaustion attacks.

**Upstream → Client** (per-session reader goroutine):
1. `Read(buf)` on session's upstream socket — blocks until response or socket closed.
2. `WriteToUDP(buf[:n], clientAddr)` on listener socket.
3. Refresh idle timer.
4. Loop.

### [D9] Input first_datagram flow state behavior

When input is `first_datagram` mode (upstream sends `[PROXY][P1]`, then `[P2]`, `[P3]`...), ProxyDge must recover the normalized source for headerless packets:

```
P1 = [PROXY header][payload]  → parse header → NormalizedDatagram with Source=V2
                                 → decide() passes → persist inputSource (deep-copied)
P2 = [payload]               → no signature → recover inputSource from session
P3 = [payload]               → same session → use stored source mapping
```

**Session stores input-side source mapping** (only for `first_datagram` input):
```go
type UDPSession struct {
    // ... routing/lifecycle fields ...
    inputSource   atomic.Pointer[proxyproto.Header]  // deep-copied, stored AFTER trust check
    inputSrcKind  atomic.Pointer[proxyproto.Source]  // V1 or V2
}
```

When a headerless datagram arrives for an existing session:
1. If `inputSource` is set → use it as the normalized source (the PROXY header from the first datagram).
2. If `inputSource` is nil → direct datagram, use `ActualPeer` as source.

**SECURITY INVARIANT — input flow state**:
1. `inputSource` may ONLY be persisted AFTER the PROXY header has passed the `ActualPeer` trust check and policy decision (step 8 in D8 pipeline).
2. Untrusted PROXY metadata must NEVER be persisted as session state. If trust check fails, the datagram is dropped (step 6) — `inputSource` is never set.
3. A new session always starts with nil `inputSource` — must re-accept PROXY header on first datagram.
4. Session recreation must NEVER inherit old `inputSource` state.

**INVARIANT — header ownership / buffer lifetime**:
1. Any `proxyproto.Header` persisted across datagrams (especially `inputSource`) MUST deep-copy `SrcIP` and `DstIP` byte slices. The `ReadFromUDP` packet buffer is reused for the next datagram — retained references would silently corrupt.
2. Payload may remain a packet-buffer slice ONLY while the current datagram is synchronously processed (steps 1–11 in D8). It must NOT be retained asynchronously (e.g., stored in session state for later forwarding).
3. **Rule: "Persisted session metadata must own its address byte slices."**

Deep-copy implementation:
```go
func cloneHeader(h proxyproto.Header) proxyproto.Header {
    return proxyproto.Header{
        SrcIP:   append([]byte(nil), h.SrcIP...),
        DstIP:   append([]byte(nil), h.DstIP...),
        SrcPort: h.SrcPort,
        DstPort: h.DstPort,
        Family:  h.Family,
        TLVs:    nil, // TLVs not populated or forwarded in initial design
    }
}
```

**Session expiry must clear input flow state.** Source port reuse is a real risk — the OS may assign the same source port to a different client after expiry. If old session's `inputSource` leaked to a new session, an attacker could spoof their source. Therefore:
- `expire()` clears `inputSource` and `inputSrcKind` (set to nil).
- New session after expiry starts with nil `inputSource`.
- No old session state leaks to new session.

### [D10] Timeout / cleanup

**Idle timeout**: configurable, default 30s.

```go
func (s *UDPSession) expire() {
    s.once.Do(func() {
        close(s.done)              // signal reader goroutine
        s.idleTimer.Stop()
        s.inputSource.Store(nil)   // clear input flow state (security)
        _ = s.upstream.Close()      // unblocks reader's Read()
    })
}
```

- `idleTimer` fires after `idleTimeout` inactivity → `expire()`.
- Idempotent via `sync.Once` — safe from timer + explicit close.
- Closing upstream socket unblocks reader goroutine.
- `inputSource` and `inputSrcKind` set to nil on expiry (security invariant — prevents source port reuse leakage).
- Session removed from manager's map after `expire()` completes — no window where a new session for the same key could inherit old state (old session is fully expired and removed before `LoadOrStore` can create a new one).
- Session removed from manager's map after `expire()`.

**Graceful shutdown**:
1. Close listener socket (stops `ReadFromUDP`).
2. Iterate all sessions, call `expire()`.
3. `WaitGroup.Wait()` for all reader goroutines.

### [D11] Max session limit

```go
type UDPSessionManager struct {
    sessions    sync.Map     // sessionKey → *UDPSession
    count       atomic.Int64
    maxSessions int64
}
```

- `maxSessions` configurable, default 1024.
- At capacity: new datagrams from new sources dropped (Debug log). Existing sessions unaffected.
- Count decremented on session expiry.
- No LRU eviction — drop when full.

### [D12] PROXY Protocol v2 wire semantics for UDP

**Wire format**:
- Signature: same 12-byte magic.
- Version+Command: `0x21` (v2 + PROXY).
- Family+Transport: `0x12` (IPv4+UDP), `0x22` (IPv6+UDP).
- Address payload: same layout as TCP.
- Header size: 28 bytes (IPv4), 52 bytes (IPv6).

**New proxyproto types**:
```go
const (
    FamilyUnspec Family = iota
    FamilyTCP4
    FamilyTCP6
    FamilyUDP4  // AF_INET + SOCK_DGRAM
    FamilyUDP6  // AF_INET6 + SOCK_DGRAM
)
```

**HeaderFromAddrs** handles both TCP and UDP:
```go
func HeaderFromAddrs(src, dst net.Addr) Header {
    switch s := src.(type) {
    case *net.TCPAddr:
        d := dst.(*net.TCPAddr)
        return buildTCPHeader(s, d)
    case *net.UDPAddr:
        d := dst.(*net.UDPAddr)
        return buildUDPHeader(s, d)
    }
    return Header{Family: FamilyUnspec}
}
```

**goproxyproto datagram adapter**:
```go
func (r datagramReader) ParseDatagram(data []byte) (pp.Header, []byte, pp.Source, error) {
    // Check for PROXY v2 signature prefix
    // If present: parse header, extract payload, return (hdr, payload, V2, nil)
    // If absent: return (zero, data, Direct, nil)
    // If malformed: return (zero, nil, 0, err) — caller MUST drop
}

func (w datagramWriter) FormatDatagram(hdr pp.Header, payload []byte) ([]byte, error) {
    // Encode PROXY v2 header + payload into a single []byte
    // Uses net.UDPAddr for UDP families, net.TCPAddr for TCP families
    // go-proxyproto's HeaderProxyFromAddrs infers SOCK_DGRAM from *net.UDPAddr
}
```

**Output encoding by mode**:
- `every_datagram`: `FormatDatagram(hdr, payload)` on every datagram.
- `first_datagram`: `FormatDatagram(hdr, payload)` on first datagram, raw `payload` on subsequent. `headerSent atomic.Bool` tracks this (output state only).

### [D13] Trust/security invariant

**SECURITY INVARIANT**: TrustChecker decisions are based on `ActualPeer` (the real socket source from `ReadFromUDP`), NEVER the PROXY header's claimed SrcIP.

```
ActualPeer (socket source)
    │
    ▼
trust.IsTrusted(transport.RemoteIP(actualPeer))
    │
    ├── trusted → PROXY header's Source is accepted as the normalized Source
    └── untrusted → PROXY header is rejected/stripped; ActualPeer becomes the Source
```

- `transport.RemoteIP` extended: type switch handles `*net.TCPAddr` and `*net.UDPAddr`.
- `decide()` receives `ActualPeer`'s IP, not the PROXY header's SrcIP.
- If no trusted PROXY header: `ActualPeer` is the direct source — `HeaderFromAddrs(actualPeer, listenerAddr)` builds the header.
- Session expiry clears input flow state — prevents source port reuse attacks (see [D9]).

### [D14] Failure / recreation behavior

| Scenario | Behavior |
|----------|----------|
| `DialUDP` fails | Datagram dropped (Warn). No session created. |
| Upstream `Write` fails | Datagram dropped (Debug). **Session stays — does NOT trigger expiry.** UDP write errors are typically transient (ICMP port unreachable, temporary buffer full). The session's upstream socket remains valid. |
| Oversized datagram | Dropped (Debug). Never truncated. |
| Upstream `Read` errors | Reader goroutine exits. Session expires. Next datagram creates fresh session. |
| Idle timer fires | `expire()`: upstream closed, reader exits, input state cleared, removed from map. |
| Max sessions reached | New datagrams from new sources dropped (Debug). |
| Listener socket error | `Serve()` returns error. Main shuts down or recreates listener. |
| Shutdown signal | All sessions expired. Reader goroutines joined via WaitGroup. |

**No state carries over on recreation** (security invariant): new session after expiry starts with nil `inputSource` and `headerSent=false`. Enforced by:
1. `expire()` clears `inputSource`/`inputSrcKind` to nil (inside `sync.Once`).
2. `manager.remove(key)` removes session from `sync.Map` (inside same `sync.Once` block).
3. `LoadOrStore` for a new session can only succeed after the old session is fully removed.
4. No window where old and new sessions coexist for the same key.
This prevents source port reuse attacks: a different client reusing the same source port after expiry gets a fresh session, never inheriting the old client's PROXY metadata.

### [D15] Concurrency / race considerations

**Architecture: 1 listener goroutine + N reader goroutines (per session).**

**Race 1: Concurrent datagrams from same source**
- `sync.Map.LoadOrStore` — exactly one session created.

**Race 2: Session expiry vs. datagram forwarding**
- `expire()` closes upstream socket via `sync.Once`.
- Listener's `Write()` to closed socket errors — datagram dropped.
- Reader's `Read()` unblocks — exits.

**Race 3: Session expiry vs. reader goroutine**
- `expire()` closes `done` channel + upstream socket.
- Reader checks `done` before `WriteToUDP` — no write after close.

**Race 4: Session map removal vs. new datagram**
- `sync.Map.Load` returns expiring session (done closed → drop) or nil (new session).

**Race 5: Max session count**
- `atomic.Int64` count + `LoadOrStore` with rollback on race.

**Race 6: Input flow state (`first_datagram` input mode)**
- Only listener goroutine reads/writes `inputSource` (it's the only one processing client→upstream).
- No race — single writer.
- `expire()` clears `inputSource` via `atomic.Pointer.Store(nil)` — safe concurrent with listener's read (listener sees nil → treats as direct).

**Race 7: `headerSent` (`first_datagram` output mode)**
- Only listener goroutine writes `headerSent`.
- No race — single writer.

**Race 8: Idle timer reset**
- Both listener and reader call `refresh()`.
- `time.Timer.Reset` safe for concurrent use (Go 1.23+).
- `sync.Once` prevents double-expire.

**Race 9: Session removal vs. new session creation (remove timing)**
- Old session expires: `expire()` runs → clears state → closes upstream → reader exits.
- Then `manager.remove(key)` removes from `sync.Map`.
- A new datagram for the same key: `sync.Map.Load` returns nil (old removed) → new session via `LoadOrStore`.
- Key invariant: old session is fully expired AND removed from map BEFORE new session can be created. No window where `LoadOrStore` returns the old (expiring) session to a new datagram, because `remove` happens inside `expire()`'s `sync.Once` block — the session is atomically removed and state-cleared together.
- If `Load` returns the expiring session (still in map but `done` closed): listener drops the datagram (session is expired). Next datagram creates a fresh session.

**Race 10: Resource ordering — decide() before session creation**
- The listener goroutine runs decide() (step 5) BEFORE creating a session (step 7).
- For new sources: no session exists during decide(). No race — decide() operates on the NormalizedDatagram, not on session state.
- For existing sessions: `inputSource` is read (step 4) before decide() (step 5). `inputSource` is an `atomic.Pointer` — safe concurrent read with `expire()`'s `Store(nil)`. If expire() clears it mid-read, decide() sees nil → treats as direct → uses ActualPeer. This is safe (the session is expiring anyway).

### [D16] Test matrix: compatibility layer regression

Input × Output combinations:

| Input ↓ \ Output → | every_datagram | first_datagram |
|---------------------|----------------|----------------|
| direct              | direct→every   | direct→first   |
| first_datagram      | first→every    | first→first    |
| every_datagram      | every→every    | every→first    |

**Verify for each combination**:
- Payload bytes unchanged through the pipeline.
- Normalized source correct (from PROXY header if trusted, from ActualPeer if direct/untrusted).
- Header placement correct (every vs first-only).
- Datagram boundary preserved (no merging, no splitting).
- Malformed PROXY datagram dropped (NOT treated as payload).
- Session expiry → new session does NOT inherit old input source mapping.
- Session expiry → `headerSent` reset (for first_datagram output).

Additional tests:
- Malformed PROXY signature → drop, never fallback.
- Max sessions → new sources dropped.
- Idle timeout → session expires, reader exits.
- Trust check: untrusted source with PROXY header → reject/strip.
- Trust check: spoofed SrcIP in PROXY header → rejected (ActualPeer used, not header).
- **Resource ordering**: untrusted source with PROXY header + policy=reject → NO session created, NO DialUDP, NO goroutine. Verify zero resource consumption on rejection.
- **Header ownership**: persist `inputSource` from first_datagram input, then send many more datagrams. Verify the stored Header's SrcIP/DstIP byte slices are independent copies — modify the packet buffer and confirm stored Header is unaffected.
- **inputSource security**: untrusted source sends first_datagram with PROXY header → rejected → verify `inputSource` is nil (untrusted metadata never persisted).
- **Session recreation isolation**: session expires → new datagram from same source creates new session → verify new session has nil `inputSource` (no inheritance). Then send a new first PROXY datagram → verify it's accepted as a fresh first-datagram.
- **IPv6 zone session-key**: two clients on different interfaces with the same link-local address (`fe80::1%eth0` vs `fe80::1%wlan0`) → verify distinct sessions created (zone distinguishes them).
- **Oversized datagram**: datagram exceeding max datagram size → dropped (Debug). Never truncated. No PROXY header parsed from a truncated datagram.
- **Upstream Write error**: simulate write error → datagram dropped but session stays alive (no expiry triggered). Verify subsequent datagrams still forwarded on the same session.

### Config changes (design only)

```yaml
protocol: udp
listen: ":9000"
upstream: "127.0.0.1:9001"
idle-timeout: 30s
max-sessions: 1024
max-datagram-size: 65535          # drop oversized, never truncate
proxy-protocol:
  udp-input: auto                    # always auto-detect
  udp-output: every_datagram         # default; first_datagram for compat
```

### Struct sketches (design only)

```go
type UDPGateway struct {
    listener        *net.UDPConn
    upstreamAddr    *net.UDPAddr   // pre-resolved in New()/Serve() startup
    dgReader        proxyproto.DatagramReader
    dgWriter        proxyproto.DatagramWriter
    policy          gateway.Policy
    trust           *gateway.TrustChecker
    untrusted       gateway.UntrustedAction
    outputMode      OutputMode          // EveryDatagram | FirstDatagram
    idleTimeout     time.Duration
    maxSessions     int64
    maxDatagramSize int                 // drop oversized, never truncate
    log             *slog.Logger
    manager         *UDPSessionManager
}

type UDPSession struct {
    key           sessionKey
    clientAddr    *net.UDPAddr         // for WriteToUDP responses
    upstream      *net.UDPConn         // connected UDP socket (DialUDP to pre-resolved upstreamAddr)
    idleTimer     *time.Timer
    done          chan struct{}
    once          sync.Once
    // first_datagram OUTPUT state (only when outputMode == FirstDatagram)
    headerSent    atomic.Bool
    // first_datagram INPUT state (only when input is first_datagram mode)
    // Persisted ONLY after trust check passes. Deep-copied via cloneHeader.
    inputSource   atomic.Pointer[proxyproto.Header]
    inputSrcKind  atomic.Pointer[proxyproto.Source]
    log           *slog.Logger
}
```

## [S3] Out of Scope

- No QUIC or Unix socket.
- No TCP changes — `pipeStream()`, `transport.CloseWriter`, `tcp.Conn` stay TCP-only.
- No `io.ReadWriteCloser` adaptation for UDP.
- No LRU session eviction.
- No multi-downstream routing.
- No implementation — this is a design spec for review.
- Config changes are sketched but not built.

## Tasks

- [ ] T1: Review this revised design spec — acceptance: user confirms or requests further changes (covers: S2)
- [ ] T2: (Implementation) Extend `proxyproto`: `FamilyUDP4`/`FamilyUDP6` + `HeaderFromAddrs(src, dst)` + `DatagramReader`/`DatagramWriter` interfaces — acceptance: `go build` passes; TCP behavior unchanged; `HeaderFromConn` delegates to `HeaderFromAddrs` (covers: S2; depends: T1)
- [ ] T3: (Implementation) Implement `goproxyproto` datagram adapter: `ParseDatagram` (signature check + parse + malformed=error) + `FormatDatagram` (header+payload→[]byte) — acceptance: unit tests for direct/first/every/malformed input; unit tests for every/first output encoding (covers: S2; depends: T2)
- [ ] T4: (Implementation) Extend `transport.RemoteIP` with `*net.UDPAddr` case — acceptance: `go build` passes; TCP unchanged (covers: S2; depends: T1)
- [ ] T5: (Implementation) Create `internal/udp/session.go` — `UDPSession` with lifecycle, reader goroutine, idle timer, input flow state (deep-copied Header via `cloneHeader` with TLVs=nil), `sync.Once` expiry — acceptance: compiles; `expire()` clears inputSource (nil) + inputSrcKind + removes from manager; reader exits on done (covers: S2; depends: T3, T4)
- [ ] T6: (Implementation) Create `internal/udp/manager.go` — `UDPSessionManager` with `sync.Map`, atomic count, max sessions; `remove` inside `expire()` sync.Once block; `keyFromUDPAddr()` canonicalizes IP+port+zone — acceptance: compiles; LoadOrStore + count correct; remove timing: no window for state inheritance; IPv6 zone in key (covers: S2; depends: T5)
- [ ] T7: (Implementation) Create `internal/udp/gateway.go` — `UDPGateway`, `New()` (pre-resolves upstreamAddr via `net.ResolveUDPAddr` once), `Serve()`, `handleDatagram()` — security-ordered pipeline: ParseDatagram → session lookup → recover flow state → decide() → if rejected DROP (no session) → if allowed create/forward; oversized datagram drop (never truncate); upstream Write error does NOT trigger expiry — acceptance: compiles; uses `decide()` from gateway; does NOT use `pipeStream()`/`bufio.Reader`; decide() BEFORE session creation; pre-resolved upstream; maxDatagramSize enforced (covers: S2; depends: T5, T6)
- [ ] T8: (Implementation) Add test matrix per [D16] — all 6 input×output combinations + malformed + session expiry + trust + resource ordering + header ownership (deep-copy) + inputSource security + recreation isolation + **IPv6 zone session-key** + **oversized datagram drop** + **upstream Write error no-expiry** — acceptance: all combinations pass; all security invariants verified; all edge cases verified (covers: S2; depends: T7)
- [ ] T9: (Implementation) Wire to `main.go` with `protocol: udp` config — acceptance: `proxydge start` with UDP config runs the UDP gateway (covers: S2; depends: T8)
