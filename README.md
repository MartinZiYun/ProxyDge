[English](README.md) | [简体中文](README.zh-CN.md)

# ProxyDge

A PROXY Protocol normalizing gateway.

Listens on a single TCP port, accepts upstream connections (direct / PROXY Protocol v1 / v2), normalizes them all to PROXY Protocol v2, and forwards to a single configurable downstream. The downstream therefore always receives a uniform v2 header — the upstream protocol variant differences are absorbed by this service.

## Features

- **Protocol normalization**: direct, PROXY v1, PROXY v2 → uniform PROXY v2 output
- **Source trust control**: only configured trusted IP networks may send PROXY headers, preventing address spoofing
- **Policy control**: `use` (default, accept all three) / `require` (PROXY header mandatory) / `reject` (no PROXY header allowed)
- **Config auto-migration**: automatically upgrades old config files when new fields are added — backs up the original, preserves unknown fields
- **Multi-language**: `en` (default), `zh-CN` (Simplified Chinese), `zh-TW` (Traditional Chinese)
- **Single-file deployment**: locale files embedded via `go:embed`, no external dependencies
- **Cross-platform**: Linux + Windows × amd64 + arm64

## Quick Start

### Download

Download the binary for your platform from the [Releases](https://github.com/MartinZiYun/ProxyDge/releases) page.

### Configure

```bash
# Generate a sample config
./proxydge init

# Edit the config file (fill in upstream)
vi config.yaml
```

Sample config file:

```yaml
version: 1

listen: ":9000"                    # listen address
upstream: "127.0.0.1:9001"        # downstream target (required)
policy: "use"                      # use | require | reject
detect-timeout: "1s"               # PROXY header detection timeout
lang: ""                           # display language: en | zh-CN | zh-TW (empty = auto-detect)

# Source trust control: only these networks may send PROXY headers
# Empty (default) trusts everyone — configure in production to prevent spoofing
trusted-networks:
  - "10.0.0.0/8"
untrusted-proxy-action: "reject"   # reject (default) | strip (strip header, re-normalize as direct)

log:
  console:
    level: "info"                  # debug | info | warn | error
    format: "text"                 # text | json
  file:
    path: ""                       # empty = file logging disabled
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
| `-listen <addr>` | `:9000` | Listen address |
| `-upstream <host:port>` | (required) | Downstream target |
| `-policy <p>` | `use` | `use` \| `require` \| `reject` |
| `-detect-timeout <dur>` | `1s` | PROXY header detection timeout |
| `-trusted-networks <cidrs>` | (empty = trust all) | Trusted networks, comma-separated CIDRs |
| `-untrusted-proxy-action <a>` | `reject` | `reject` \| `strip` |
| `-config <path>` | `<exe-dir>/config.yaml` | Config file path |
| `-lang <locale>` | auto-detect | `en` \| `zh-CN` \| `zh-TW` |
| `-log-console-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-console-format <f>` | `text` | `text` \| `json` |
| `-log-file <path>` | (empty = disabled) | File log path |
| `-log-file-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-file-format <f>` | `json` | `text` \| `json` |

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

Every CLI flag has a corresponding environment variable (prefix `PROXYDGE_`, uppercase, `-` replaced with `_`):

```bash
PROXYDGE_UPSTREAM=127.0.0.1:9001
PROXYDGE_POLICY=require
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8,192.168.0.0/16
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
PROXYDGE_LANG=zh-CN
```

### Config File Discovery

1. `-config <path>` explicit (file must exist, otherwise error)
2. `config.yaml` next to the executable (auto-discovered, silently skipped if absent)

## Source Trust Control

ProxyDge's `Policy` (use/require/reject) controls whether PROXY headers are accepted. **Trust control** controls *who is allowed to send them*.

### How It Works

1. Detect whether the connection carries a PROXY header
2. If a header is present and the source IP is **not in a trusted network** → handle per `untrusted-proxy-action`:
   - `reject` (default): close the connection immediately
   - `strip`: strip the header, re-normalize using the TCP socket's real peer address as a direct connection
3. Then apply policy as usual

**Security-critical**: The IP used for trust checking **always comes from the TCP socket's `RemoteAddr()`**, never from the source address claimed in the PROXY header. An attacker cannot bypass the check by forging a trusted IP inside the header.

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

## Config Auto-Migration

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
internal/gateway/                        # gateway: accept loop + trust check + normalization + bidirectional pipe
  ├── gateway.go                         # Gateway.Serve + handle + Policy + TrustChecker
  └── trust.go                           # TrustChecker + UntrustedAction
internal/proxyproto/                     # PROXY Protocol abstraction (interfaces + types)
  └── goproxyproto/                      # go-proxyproto library adapter (library only imported here)
internal/transport/                      # transport abstraction (Conn/Listener/Dialer interfaces + TCP adapter)
internal/i18n/                           # internationalization (go:embed + locale YAML)
  └── locales/                           # en.yaml / zh-CN.yaml / zh-TW.yaml
internal/version/                        # version info (ldflags injection)
```

### Library Isolation

Business code does not directly import `github.com/pires/go-proxyproto`. The library is isolated behind the `internal/proxyproto/goproxyproto` adapter subpackage — swapping the library only requires changing that one subpackage.

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
