<p align="center">
  <h1 align="center">ProxyDge</h1>
  <p align="center">A PROXY Protocol normalizing gateway for TCP and UDP.</p>
</p>

Listens on a port, accepts upstream connections/datagrams (direct / PROXY Protocol v1 / v2), normalizes them all to a single configurable PROXY Protocol version, and forwards to a downstream. The upstream protocol variant differences are absorbed by this service.

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/License-GPLv3-blue.svg" alt="License">
  <img src="https://img.shields.io/github/release/MartinZiYun/ProxyDge" alt="Release">
</p>

[English](README.md) | [简体中文](README.zh-CN.md)

## Features

- **Dual-protocol**: TCP (byte-stream with half-close) and UDP (datagram with session model) — each with its own dedicated gateway, no shared stream abstraction
- **Protocol normalization**: direct, PROXY v1, PROXY v2 → uniform output version of choice (`tcp.header-version`), plus explicit policy for self-contradictory mixed-address-family headers
- **IPv4/IPv6 dual-stack**: full IPv6 support including link-local zone identifiers for UDP session routing
- **Source trust control**: only configured trusted IP networks may send PROXY headers, preventing address spoofing. Supports CIDR notation and bare IPs (IPv4/IPv6)
- **Policy control**: `use` (default, accept all three) / `require` (PROXY header mandatory) / `reject` (no PROXY header allowed)
- **UDP session management**: per-session connected upstream sockets, idle timeout, max session limit, configurable PROXY header emission mode
- **Config auto-migration**: automatically upgrades old config files when new fields are added — backs up the original, preserves unknown fields
- **Multi-language**: `en` (default), `zh-CN` (Simplified Chinese), `zh-TW` (Traditional Chinese)
- **Single-file deployment**: locale files embedded via `go:embed`, no external dependencies
- **Cross-platform**: Linux + Windows × amd64 + arm64

## Quick Start

### Download

Download the binary for your platform from the [Releases](https://github.com/MartinZiYun/ProxyDge/releases) page.

Dev builds: open the latest successful run in the [Actions list](https://github.com/MartinZiYun/ProxyDge/actions/workflows/dev-build.yml), then download your platform's binary from the Artifacts section.

### Configure

```bash
# Generate a sample config
./proxydge init

# Edit the config file
vi config.yaml
```

Sample config file:

```yaml
version: 3  # do NOT change; used for auto-migration

# ── General ───────────────────────────────────────────────────────────
protocol: "tcp"                      # tcp (default) | udp — selects gateway mode
listen: ":9000"                      # listen address (host:port)
upstream: "127.0.0.1:9001"          # downstream target host:port
policy: "use"                        # use | require | reject
lang: ""                             # display language: en|zh-CN|zh-TW (empty=auto)

# Trust control: only these networks may send PROXY headers.
# Supports CIDR (10.0.0.0/8, 2001:db8::/32) and bare IPs (10.0.0.1, fe80::1).
# Empty (default) trusts everyone — configure in production to prevent spoofing.
trusted-networks:
  # - "10.0.0.0/8"
  # - "2001:db8::/32"
  # - "10.0.0.1"        # bare IP → /32 (IPv4) or /128 (IPv6)
untrusted-proxy-action: "reject"     # reject (default) | strip

# ── TCP (protocol=tcp) ───────────────────────────────────────────────
tcp:
  detect-timeout: "1s"               # PROXY header detection timeout (0=block indefinitely)
  idle-timeout: "5m"               # pipe idle timeout, 0=disabled
  header-version: "v2"               # downstream PROXY header version: v1|v2
  family-mismatch: "reject"          # mixed address-family action: reject|unknown|legacy
  max-connections: 4096              # max concurrent connections, 0=unlimited

# ── UDP (protocol=udp) ───────────────────────────────────────────────
# The following fields are only used when protocol=udp.
udp:
  idle-timeout: "30s"               # UDP session idle timeout
  max-sessions: 1024                # max concurrent UDP sessions
  max-datagram-size: 65535          # max datagram size, 0=unlimited, oversized=drop
  header-mode: every_datagram       # every_datagram (default) | first_datagram

# ── Logging ──────────────────────────────────────────────────────────
log:
  console:                          # logs to stderr
    level: "info"                    # debug | info | warn | error
    format: "text"                   # text | json
  file:                             # logs to a file (path empty => disabled)
    path: ""                         # e.g. /var/log/proxydge.log
    level: "info"
    format: "json"
```

### Run

```bash
./proxydge start
```

On startup, version info, config sources, and security warnings (if any) are printed.

## CLI Commands

```
proxydge <command> [options]

  start     Run the gateway
  init      Generate a sample config.yaml
  version   Print version and build info
  help      Show help
```

Running `./proxydge` with no arguments is equivalent to `help`.

### start Options

| Option | Default | Description |
|--------|---------|-------------|
| `-protocol <p>` | `tcp` | `tcp` \| `udp` — selects gateway mode |
| `-listen <addr>` | `:9000` | Listen address |
| `-upstream <host:port>` | `127.0.0.1:9001` | Downstream target |
| `-policy <p>` | `use` | `use` \| `require` \| `reject` |
| `-trusted-networks <cidrs>` | (empty = trust all) | Trusted networks, comma-separated CIDRs or bare IPs |
| `-untrusted-proxy-action <a>` | `reject` | `reject` \| `strip` |
| `-config <path>` | `<exe-dir>/config.yaml` | Config file path |
| `-lang <locale>` | auto-detect | `en` \| `zh-CN` \| `zh-TW` |
| `-tcp-detect-timeout <dur>` | `1s` | PROXY header detection timeout (0=block indefinitely) |
| `-tcp-idle-timeout <dur>` | `5m` | Pipe idle timeout (0=disabled) |
| `-tcp-header-version <v>` | `v2` | Downstream PROXY header version: `v1` \| `v2` |
| `-tcp-family-mismatch <a>` | `reject` | Mixed address-family action: `reject` \| `unknown` \| `legacy` |
| `-tcp-max-connections <n>` | `4096` | Max concurrent connections, over-limit accept closed; `0` = unlimited |
| `-udp-idle-timeout <dur>` | `30s` | UDP session idle timeout |
| `-udp-max-sessions <n>` | `1024` | Max concurrent UDP sessions; `0` = unlimited |
| `-udp-max-datagram-size <n>` | `65535` | Max datagram size (0=unlimited) |
| `-udp-header-mode <m>` | `every_datagram` | `every_datagram` \| `first_datagram` |
| `-log-console-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-console-format <f>` | `text` | `text` \| `json` |
| `-log-file <path>` | (empty = disabled) | File log path |
| `-log-file-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-file-format <f>` | `text` | `text` \| `json` |

### init Options

| Option | Description |
|--------|-------------|
| `-config <path>` | Where to write the sample config (default: `<exe-dir>/config.yaml`) |
| `-force` | Overwrite an existing config file (refused by default) |

### version Options

```bash
./proxydge version           # detailed multi-line info
./proxydge version --short   # just the version, e.g. v0.1.0
```

## Configuration

### Precedence

From highest to lowest:

```
CLI flags  >  env (PROXYDGE_*)  >  config file  >  defaults
```

### Environment Variables

Every CLI flag has a corresponding environment variable (prefix `PROXYDGE_`, uppercase, `-` replaced with `_`). Protocol-specific flags use `TCP_`/`UDP_` prefixes:

```bash
PROXYDGE_PROTOCOL=udp
PROXYDGE_UPSTREAM=127.0.0.1:9001
PROXYDGE_POLICY=require
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8,192.168.0.0/16
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
PROXYDGE_LANG=zh-CN
PROXYDGE_TCP_DETECT_TIMEOUT=2s
PROXYDGE_TCP_HEADER_VERSION=v1
PROXYDGE_TCP_FAMILY_MISMATCH=unknown
PROXYDGE_TCP_MAX_CONNECTIONS=2048
PROXYDGE_UDP_IDLE_TIMEOUT=60s
PROXYDGE_UDP_MAX_SESSIONS=2048
PROXYDGE_UDP_MAX_DATAGRAM_SIZE=1500
PROXYDGE_UDP_HEADER_MODE=first_datagram
```

### Config File Discovery

1. `-config <path>` explicit (file must exist, otherwise error)
2. `config.yaml` next to the executable (auto-discovered, silently skipped if absent)

## Source Trust Control

ProxyDge's `Policy` (use/require/reject) controls whether PROXY headers are accepted. **Trust control** controls *who is allowed to send them*.

### How It Works

1. Detect whether the connection/datagram carries a PROXY header
2. If a header is present and the source IP is **not in a trusted network** → handle per `untrusted-proxy-action`:
   - `reject` (default): close the connection / drop the datagram immediately
   - `strip`: strip the header, re-normalize using the socket's real peer address as a direct connection
3. Then apply policy as usual

**Security-critical**: The IP used for trust checking **always comes from the socket's real peer address** (`RemoteAddr()` for TCP, `ReadFromUDP` for UDP), never from the source address claimed in the PROXY header. An attacker cannot bypass the check by forging a trusted IP inside the header.

### Trust Network Configuration

`trusted-networks` accepts both CIDR notation and bare IP addresses:

```yaml
trusted-networks:
  - "10.0.0.0/8"           # IPv4 CIDR
  - "2001:db8::/32"        # IPv6 CIDR
  - "fe80::/10"            # IPv6 link-local CIDR
  - "10.0.0.1"             # bare IPv4 → auto-converted to /32
  - "2001:db8::1"          # bare IPv6 → auto-converted to /128
```

### Behavior Matrix

| Source | Has PROXY header | untrusted-proxy-action | Result |
|--------|-----------------|----------------------|--------|
| Trusted IP | Yes | any | Header trusted, forwarded normally |
| Trusted IP | Malformed | any | Rejected (trust does not bypass protocol validation) |
| Untrusted IP | Yes | reject | Connection rejected |
| Untrusted IP | Yes | strip | Header stripped, forwarded with real IP |
| Untrusted IP | No (direct) | any | Forwarded normally (trust check not triggered) |

### Startup Warnings

- `trusted-networks` empty → warns that all sources are trusted, posing a spoofing risk
- `untrusted-proxy-action=strip` → warns that untrusted sources can still connect
- `tcp.family-mismatch=legacy` → warns that historical auto-conversion is kept for mismatched headers (`::ffff:`-mapped IPv4 may reach downstream labeled as IPv6)

## TCP Output Version & Address-Family Mismatch

### `tcp.header-version` — normalization target

ProxyDge accepts direct connections and PROXY v1/v2 headers upstream, then re-encodes every connection into ONE version for the downstream:

- `v2` (default): binary header — full address-family coverage, extensible
- `v1`: text header (`PROXY TCP4 ...` / `PROXY TCP6 ...`) — for downstream services that only understand the legacy text format

Inherent v1 limits: no TLV support, and a source/destination family mix cannot be represented faithfully.

### `tcp.family-mismatch` — when a header contradicts itself

A PROXY header declares ONE address family covering both addresses. A crafted or buggy upstream can send a header declaring INET6 whose destination is really an IPv4 address in `::ffff:` mapped form. Re-encoding it silently coerces the address — downstream may treat `::ffff:192.168.1.1` as a genuine IPv6 target and fail or misconnect. ProxyDge never fabricates addresses; you choose the disposition:

| Value | Behavior |
|-------|----------|
| `reject` (default) | Close the connection before dialing downstream; log the rejection |
| `unknown` | Forward an address-less header — `PROXY UNKNOWN` with v1 output, LOCAL+AF_UNSPEC with v2 — so downstream falls back per spec |
| `legacy` | Skip the check entirely; keep the historical byte-for-byte behavior including silent `::ffff:` mapping. Prints a startup WARNING |

The check runs on the FINAL header after trust/policy processing: direct connections and stripped untrusted headers are rebuilt from real socket addresses, which are always self-consistent — they are never affected.

## UDP Gateway

When `protocol: udp`, ProxyDge runs as a UDP PROXY Protocol gateway with its own datagram/session model:

- **Per-session connected upstream sockets**: each client session gets a dedicated `DialUDP` socket — the OS kernel filters responses by source, no application-level routing table needed
- **Session lifecycle**: NEW → ACTIVE → EXPIRED (idle timeout). Sessions are keyed by `(sourceIP, sourcePort, IPv6 zone)`
- **Max session limit**: configurable cap on concurrent sessions (default 1024, `0` = unlimited); new sessions from new sources are dropped when at capacity
- **PROXY header emission modes** (`udp.header-mode`):
  - `every_datagram` (default): each datagram carries a PROXY v2 header — downstream is stateless
  - `first_datagram`: only the first datagram in a session carries a header — lower overhead, requires downstream flow state
- **Input auto-detection**: the gateway auto-detects whether incoming datagrams carry PROXY headers (direct / first_datagram / every_datagram) — controlled by the `policy` field, no separate input config needed
- **Malformed = drop**: if a datagram starts with a PROXY v2 signature but fails to parse, it is dropped — never falls back to treating it as payload
- **Resource ordering**: trust + policy decisions run BEFORE session creation — rejected sources consume zero resources (no socket, no goroutine, no session slot)
- **IPv6 Zone**: link-local addresses with different zone identifiers (e.g., `fe80::1%eth0` vs `fe80::1%eth1`) are treated as different sessions. Note: the PROXY Protocol v2 wire format does not support zone identifiers — zones are preserved locally for session routing but not transmitted to the downstream.

### Config Auto-Migration

The config file includes a `version` field marking its format version.

- **Same version**: no action
- **Older version**: auto-migrate — back up the original file to `.bak`, regenerate the config with all fields + comments, **preserve unknown fields verbatim**, print a `NOTICE` on startup
- **Newer version**: error (ProxyDge may have been downgraded — please upgrade)
- **Missing version field**: error (run `proxydge init` to generate a versioned config)

Migration guarantees the **backup succeeds before the original file is written**, so data is never lost on write failure.

`proxydge init` refuses to overwrite an existing file (use `-force`). To upgrade an old config, simply run `proxydge start` — it will auto-migrate.

## Multi-language Support

ProxyDge supports three display languages:

| Language | Code |
|----------|------|
| English | `en` |
| Simplified Chinese | `zh-CN` |
| Traditional Chinese | `zh-TW` |

Language selection precedence:

```
--lang flag  >  PROXYDGE_LANG env  >  system locale (LANG/LC_ALL)  >  en
```

```bash
# Option 1: flag
proxydge start -lang zh-CN
proxydge help -lang zh-TW

# Option 2: env var
PROXYDGE_LANG=zh-CN proxydge start

# Option 3: system locale (Linux)
export LANG=zh_CN.UTF-8
proxydge start
```

Unsupported locales automatically fall back to English. Missing translation keys fall back to English; if English is also missing, `[missing:key]` is displayed — never panics.

## Architecture

```
main.go                                  # composition root: flag parsing, adapter wiring, signal handling
internal/config/                         # config loading (defaults < file < env < flags)
internal/protocol/                       # Protocol enum (tcp/udp) — config/routing label only
internal/transport/                      # cross-transport interfaces (AddrConn, Conn, CloseWriter, RemoteIP)
internal/tcp/                            # TCP-specific: Conn (stream + half-close), Listener, Dialer
internal/udp/                            # UDP-specific: Gateway, Session, SessionManager (datagram model)
internal/gateway/                        # TCP gateway + shared decision logic
  ├── gateway.go                         # Gateway.Serve + handle + Policy
  ├── decide.go                          # Decide() — trust + policy (shared TCP/UDP)
  ├── pipe.go                            # pipeStream() — TCP bidirectional pipe
  └── trust.go                           # TrustChecker + UntrustedAction
internal/proxyproto/                     # PROXY Protocol abstraction (interfaces + types)
  └── goproxyproto/                      # go-proxyproto library adapter (library only imported here)
    ├── reader.go                        # TCP stream reader
    ├── writer.go                        # TCP stream writer
    └── datagram.go                      # UDP datagram reader/writer
internal/i18n/                           # internationalization (go:embed + locale YAML)
  └── locales/                           # en.yaml / zh-CN.yaml / zh-TW.yaml
internal/version/                        # version info (ldflags injection)
```

### Design Principles

- **TCP and UDP have separate models**: UDP does NOT adapt into `io.ReadWriteCloser`/`pipeStream`. TCP is byte-stream with half-close; UDP is message-oriented with session lifecycle.
- **Shared logic is transport-agnostic**: `Decide()` (trust + policy), `proxyproto.Header`/`Source`, `transport.RemoteIP` are shared. `pipeStream()` and `transport.CloseWriter` are TCP-only.
- **Library isolation**: business code does not directly import `github.com/pires/go-proxyproto`. The library is isolated behind the `internal/proxyproto/goproxyproto` adapter subpackage.
- **Security invariants**: trust decisions use the real socket peer address, never the PROXY header's claimed source. Session metadata is deep-copied. Untrusted PROXY metadata is never persisted as session state.

## Building from Source

```bash
# Install dependencies
go mod download

# Build
go build -o proxydge .

# Test
go test ./...

# Build release binaries (requires GoReleaser)
goreleaser release --snapshot --clean
```

Platforms: `linux/amd64`, `linux/arm64`, `windows/amd64`, `windows/arm64`.

## License

[GPL-3.0 license](LICENSE)
