# Source Trust Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use compose:subagent (recommended) or compose:execute to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Before starting, create an isolated worktree via the compose:worktree skill (suggested slug: `source-trust-control`).

**Goal:** Add a trust control layer to ProxyDge that restricts which IP networks may send PROXY Protocol headers, with configurable behavior for non-trusted sources.

**Architecture:** Trust check sits after PROXY header detection and before existing policy in the gateway's per-connection handler. Non-trusted sources with PROXY headers are either rejected (default) or stripped to direct (re-normalized with real socket address). Config flows through the existing Source overlay model (defaults < file < env < flags). Gateway stays config-free; main.go wires everything.

**Tech Stack:** Go stdlib (`net`, `log/slog`, `flag`, `os`), existing `proxyproto`/`transport`/`config`/`gateway` packages. No new external dependencies.

## Global Constraints

- **Library isolation:** Business code must NOT import `github.com/pires/go-proxyproto`. Library stays behind `internal/proxyproto/goproxyproto`. — from project rules
- **Config isolation:** Gateway never imports `internal/config`. main maps config fields to gateway types. — from project rules
- **Trust IP source:** Trust check uses `c.RemoteAddr()` (TCP socket real peer), NEVER the PROXY header's claimed SrcIP. — from spec S2/S3
- **Policy evaluation timing:** Policy evaluates the normalized source AFTER trust handling, not the raw presence of a PROXY header. — from spec S3
- **Commit style:** Conventional commits with Chinese subject: `feat(scope): 中文` — from project rules

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/gateway/trust.go` | Create | `UntrustedAction` enum + `TrustChecker` type + `NewTrustChecker` + `IsTrusted` |
| `internal/gateway/trust_test.go` | Create | TrustChecker unit tests |
| `internal/gateway/gateway.go` | Modify | Gateway struct (2 new fields), `New` signature, `handle` trust block, `remoteIP` helper |
| `internal/gateway/gateway_test.go` | Modify | Update `startGateway`, add `startGatewayTrusted`, add 6 integration tests |
| `internal/config/config.go` | Modify | Config struct (2 fields), sources, validate, warnings, sample, parseCIDRList |
| `internal/config/config_test.go` | Modify | Add trust config tests (validation, CSV, provenance, warnings) |
| `main.go` | Modify | TrustChecker construction, injection, warnings print, help text, `untrustedProxyAction` helper |
| `main_test.go` | Modify | Accept/invalid flag tests |

---

### Task 1: TrustChecker + UntrustedAction

**Covers:** S5 (TrustChecker + UntrustedAction)

**Files:**
- Create: `internal/gateway/trust.go`
- Test: `internal/gateway/trust_test.go`

**Interfaces:**
- Produces: `type UntrustedAction int` with `UntrustedReject`/`UntrustedStrip` + `String()`; `type TrustChecker struct`; `func NewTrustChecker(cidrs []string) (*TrustChecker, error)`; `func (t *TrustChecker) IsTrusted(ip net.IP) bool`

- [ ] **Step 1: Write the failing tests**

Create `internal/gateway/trust_test.go`:

```go
package gateway

import (
	"net"
	"testing"
)

func TestTrustCheckerNilIsTrusted(t *testing.T) {
	var tc *TrustChecker
	if !tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("nil TrustChecker should trust everyone")
	}
}

func TestTrustCheckerEmptyTrustsAll(t *testing.T) {
	tc, err := NewTrustChecker(nil)
	if err != nil {
		t.Fatalf("NewTrustChecker(nil): %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("empty trusted list should trust everyone")
	}
}

func TestTrustCheckerSingleCIDR(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("10.1.2.3 should be trusted in 10.0.0.0/8")
	}
	if tc.IsTrusted(net.ParseIP("192.168.1.1")) {
		t.Fatal("192.168.1.1 should not be trusted in 10.0.0.0/8")
	}
}

func TestTrustCheckerMultipleCIDRs(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	if !tc.IsTrusted(net.ParseIP("10.1.2.3")) {
		t.Fatal("10.1.2.3 should match first CIDR")
	}
	if !tc.IsTrusted(net.ParseIP("192.168.1.1")) {
		t.Fatal("192.168.1.1 should match second CIDR")
	}
	if tc.IsTrusted(net.ParseIP("8.8.8.8")) {
		t.Fatal("8.8.8.8 should not match any CIDR")
	}
}

func TestTrustCheckerIPv4MappedIPv6(t *testing.T) {
	tc, err := NewTrustChecker([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	ip := net.ParseIP("::ffff:10.0.0.1")
	if ip == nil {
		t.Fatal("failed to parse ::ffff:10.0.0.1")
	}
	if !tc.IsTrusted(ip) {
		t.Fatal("::ffff:10.0.0.1 should match 10.0.0.0/8 (IPv4-mapped IPv6)")
	}
}

func TestNewTrustCheckerInvalidCIDR(t *testing.T) {
	_, err := NewTrustChecker([]string{"not-a-cidr"})
	if err == nil {
		t.Fatal("invalid CIDR should return error")
	}
}

func TestUntrustedActionString(t *testing.T) {
	if UntrustedReject.String() != "reject" {
		t.Fatalf("UntrustedReject.String(): want reject, got %q", UntrustedReject.String())
	}
	if UntrustedStrip.String() != "strip" {
		t.Fatalf("UntrustedStrip.String(): want strip, got %q", UntrustedStrip.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gateway/ -run TestTrust -count=1`
Expected: FAIL — `TrustChecker` and `UntrustedAction` not defined (compile error)

- [ ] **Step 3: Write the implementation**

Create `internal/gateway/trust.go`:

```go
package gateway

import (
	"fmt"
	"net"
)

// UntrustedAction is what the gateway does when a non-trusted source
// sends a PROXY header.
type UntrustedAction int

const (
	// UntrustedReject closes the connection (default).
	UntrustedReject UntrustedAction = iota
	// UntrustedStrip consumes the header, re-normalizes as a direct
	// connection using the real TCP peer address.
	UntrustedStrip
)

func (a UntrustedAction) String() string {
	switch a {
	case UntrustedReject:
		return "reject"
	case UntrustedStrip:
		return "strip"
	}
	return "unknown"
}

// TrustChecker tests whether a remote IP is allowed to send PROXY headers.
// A nil TrustChecker or empty trusted list trusts everyone — trust control
// is opt-in.
type TrustChecker struct {
	nets []*net.IPNet
	all  bool // true when no networks configured (trust everyone)
}

// NewTrustChecker parses CIDR strings into a TrustChecker. An empty slice
// returns a trust-everyone checker (all=true). Invalid CIDRs return an error.
func NewTrustChecker(cidrs []string) (*TrustChecker, error) {
	if len(cidrs) == 0 {
		return &TrustChecker{all: true}, nil
	}
	nets := make([]*net.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted-networks entry %q: %w", cidr, err)
		}
		nets[i] = n
	}
	return &TrustChecker{nets: nets}, nil
}

// IsTrusted returns true if ip is in any trusted network. A nil checker or
// all=true trusts everyone.
func (t *TrustChecker) IsTrusted(ip net.IP) bool {
	if t == nil || t.all {
		return true
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gateway/ -run TestTrust -count=1 && go test ./internal/gateway/ -run TestUntrusted -count=1`
Expected: PASS (7 tests)

- [ ] **Step 5: Verify full build**

Run: `go build ./... && go vet ./internal/gateway/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/trust.go internal/gateway/trust_test.go
git commit -m "feat(gateway): 新增 TrustChecker 与 UntrustedAction 类型"
```

---

### Task 2: Gateway trust check integration

**Covers:** S3 (Pipeline), S5 (Gateway changes), S6 (Gateway integration tests)

**Files:**
- Modify: `internal/gateway/gateway.go` — Gateway struct, `New` signature, `handle` method
- Modify: `internal/gateway/gateway_test.go` — `startGateway` update, `startGatewayTrusted`, 6 new tests
- Modify: `main.go:95-99` — update `gateway.New` call site (pass nil + UntrustedReject, backward compat)

**Interfaces:**
- Consumes: `*TrustChecker`, `UntrustedAction` from Task 1
- Produces: Gateway with trust-aware `handle`; existing `startGateway` helper still works (nil trust = trust all)

- [ ] **Step 1: Update `startGateway` helper and write the first failing test**

In `internal/gateway/gateway_test.go`, update the `startGateway` function to pass the two new params:

```go
func startGateway(t *testing.T, policy Policy, upstream string) string {
	t.Helper()
	ln, err := transport.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gateway listen: %v", err)
	}
	g := New(ln, transport.TCPDialer{}, goproxyproto.NewReader(), goproxyproto.NewWriter(), policy, upstream, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, UntrustedReject)
	go func() { _ = g.Serve() }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}
```

Add `startGatewayTrusted` helper after `startGateway`:

```go
// startGatewayTrusted runs a gateway with a non-nil TrustChecker so trust
// decisions are exercised. The client connects from 127.0.0.1, so a trust
// list of "10.0.0.0/8" makes the client untrusted, while "127.0.0.0/8" makes
// it trusted.
func startGatewayTrusted(t *testing.T, policy Policy, upstream string, trustCIDRs []string, untrusted UntrustedAction) string {
	t.Helper()
	ln, err := transport.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gateway listen: %v", err)
	}
	tc, err := NewTrustChecker(trustCIDRs)
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	g := New(ln, transport.TCPDialer{}, goproxyproto.NewReader(), goproxyproto.NewWriter(), policy, upstream, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), tc, untrusted)
	go func() { _ = g.Serve() }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}
```

Add the first test (untrusted + PROXY v2 + reject → closed):

```go
func TestGatewayUntrustedReject(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// 127.0.0.1 is NOT in 10.0.0.0/8 → untrusted
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"10.0.0.0/8"}, UntrustedReject)

	v2Header := mustHex(t, tcp4HeaderHex)
	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write(v2Header)
	_, _ = c.Write([]byte("PING"))
	buf, _ := io.ReadAll(c)

	// Downstream must never be contacted.
	select {
	case got := <-recorded:
		t.Fatalf("downstream unexpectedly received %x", got)
	case <-time.After(300 * time.Millisecond):
	}
	if len(buf) != 0 {
		t.Fatalf("expected EOF (gateway closed), got %q", buf)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gateway/ -run TestGatewayUntrustedReject -count=1`
Expected: FAIL — `New` doesn't accept 10 arguments (compile error)

- [ ] **Step 3: Implement gateway changes**

In `internal/gateway/gateway.go`:

1. Add two fields to the `Gateway` struct (after `log *slog.Logger`):

```go
	trust     *TrustChecker
	untrusted UntrustedAction
```

2. Update `New` signature — add `trust *TrustChecker, untrusted UntrustedAction` after `logger *slog.Logger`:

```go
func New(ln transport.Listener, dialer transport.Dialer, r proxyproto.Reader, w proxyproto.Writer, policy Policy, upstream string, detectTimeout time.Duration, logger *slog.Logger, trust *TrustChecker, untrusted UntrustedAction) *Gateway {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Gateway{
		ln:            ln,
		dialer:        dialer,
		reader:        r,
		writer:        w,
		policy:        policy,
		upstream:      upstream,
		detectTimeout: detectTimeout,
		log:           logger,
		trust:         trust,
		untrusted:     untrusted,
	}
}
```

3. Add `remoteIP` helper at the end of the file:

```go
// remoteIP extracts the IP from the connection's real TCP peer address.
// It never uses the PROXY header's claimed SrcIP — trust decisions must be
// based on the socket's RemoteAddr.
func remoteIP(c transport.Conn) net.IP {
	if tcp, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP
	}
	return nil
}
```

4. Insert trust check block in `handle()` — after the `if err != nil` block (line ~114) and before the `switch` policy block (line ~115):

```go
	// Trust check: only trusted networks may send PROXY headers.
	// remoteIP comes from the TCP socket, never from the PROXY header's SrcIP.
	if src != proxyproto.SourceDirect && !g.trust.IsTrusted(remoteIP(c)) {
		switch g.untrusted {
		case UntrustedReject:
			g.log.Info("rejected: untrusted source with PROXY header", "remote", c.RemoteAddr(), "source", src)
			return
		case UntrustedStrip:
			g.log.Info("stripped: untrusted source PROXY header", "remote", c.RemoteAddr(), "source", src)
			src = proxyproto.SourceDirect
			hdr = proxyproto.HeaderFromConn(c)
		}
	}
```

- [ ] **Step 4: Update main.go call site**

In `main.go`, update the `gateway.New(...)` call (lines 95-99) to pass the two new params:

```go
	g := gateway.New(
		ln, transport.TCPDialer{},
		goproxyproto.NewReader(), goproxyproto.NewWriter(),
		gatewayPolicy(cfg.Policy), cfg.Upstream, cfg.DetectTimeout, logger,
		nil, gateway.UntrustedReject,
	)
```

This passes nil trust (= trust everyone) so existing behavior is unchanged until Task 4 wires the real config.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/gateway/ -run TestGatewayUntrustedReject -count=1`
Expected: PASS

- [ ] **Step 6: Write remaining 5 integration tests**

Add these to `internal/gateway/gateway_test.go`:

```go
func TestGatewayUntrustedStrip(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// 127.0.0.1 is NOT in 10.0.0.0/8 → untrusted; strip re-normalizes as direct
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"10.0.0.0/8"}, UntrustedStrip)

	v2Header := mustHex(t, tcp4HeaderHex) // claims SrcIP=192.0.2.1 (fake)
	echo, _ := dialAndExchange(t, gw, v2Header, []byte("PING"))

	select {
	case got := <-recorded:
		if !bytes.HasPrefix(got, sigV2) {
			t.Fatalf("downstream missing v2 header: got %x", got)
		}
		// Parse the emitted v2 header: SrcIP must be the real client IP
		// (127.0.0.1), NOT the fake 192.0.2.1 from the stripped header.
		h, src, err := goproxyproto.NewReader().Read(bufio.NewReader(bytes.NewReader(got)))
		if err != nil || src != proxyproto.SourceV2 {
			t.Fatalf("parse emitted header: src=%v err=%v", src, err)
		}
		if !h.SrcIP.Equal(net.IPv4(127, 0, 0, 1)) {
			t.Fatalf("stripped SrcIP: want 127.0.0.1 (real), got %s", h.SrcIP)
		}
		if !bytes.HasSuffix(got, []byte("PING")) {
			t.Fatalf("downstream missing payload: got %x", got)
		}
		if !bytes.Equal(echo, got) {
			t.Fatalf("echo != recorded: echo %x recorded %x", echo, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayUntrustedDirectNotAffected(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// 127.0.0.1 untrusted, but this is a direct connection (no PROXY header).
	// Trust check only applies to PROXY headers; direct is unaffected.
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"10.0.0.0/8"}, UntrustedReject)

	echo, _ := dialAndExchange(t, gw, nil, []byte("PING"))

	select {
	case got := <-recorded:
		if !bytes.HasPrefix(got, sigV2) || !bytes.HasSuffix(got, []byte("PING")) {
			t.Fatalf("direct + untrusted should forward normally: got %x", got)
		}
		_ = echo
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayUntrustedHeaderClaimsTrusted(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// Trust list is 10.0.0.0/8. Client connects from 127.0.0.1 (untrusted)
	// but sends a PROXY header claiming SrcIP=10.0.0.1 (inside trusted range).
	// Must still be rejected — remoteIP comes from the socket, not the header.
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"10.0.0.0/8"}, UntrustedReject)

	// Build a v2 header with SrcIP=10.0.0.1 (claims to be from trusted network)
	fakeHeader := mustHex(t, "0d0a0d0a000d0a515549540a2111000c0a000001c633640104d21f90")
	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write(fakeHeader)
	_, _ = c.Write([]byte("PING"))
	buf, _ := io.ReadAll(c)

	select {
	case got := <-recorded:
		t.Fatalf("downstream unexpectedly received %x (should be rejected)", got)
	case <-time.After(300 * time.Millisecond):
	}
	if len(buf) != 0 {
		t.Fatalf("expected EOF, got %q", buf)
	}
}

func TestGatewayTrustedProxyV2(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// 127.0.0.1 IS in 127.0.0.0/8 → trusted; PROXY header honored.
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"127.0.0.0/8"}, UntrustedReject)

	v2Header := mustHex(t, tcp4HeaderHex)
	echo, _ := dialAndExchange(t, gw, v2Header, []byte("PING"))

	select {
	case got := <-recorded:
		want := append(mustHex(t, tcp4HeaderHex), []byte("PING")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("trusted + v2: want %x, got %x", want, got)
		}
		_ = echo
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayTrustedMalformedRejected(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	// 127.0.0.1 IS trusted, but sends a malformed PROXY header (invalid version
	// byte). Trust only controls "who may send", not "is the header valid".
	// reader.Read returns err → close, before trust check.
	gw := startGatewayTrusted(t, PolicyUse, downAddr, []string{"127.0.0.0/8"}, UntrustedReject)

	// 12-byte v2 signature + version=1 (invalid, must be 2) + cmd=1 + family=TCP4 + len=12
	malformed := append(sigV2, 0x11, 0x11, 0x00, 0x0c)
	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write(malformed)
	_, _ = c.Write([]byte("PING"))
	buf, _ := io.ReadAll(c)

	select {
	case got := <-recorded:
		t.Fatalf("downstream unexpectedly received %x (malformed should be rejected)", got)
	case <-time.After(300 * time.Millisecond):
	}
	if len(buf) != 0 {
		t.Fatalf("expected EOF, got %q", buf)
	}
}
```

- [ ] **Step 7: Run all gateway tests**

Run: `go test ./internal/gateway/ -count=1 -v`
Expected: PASS (existing 6 + new 6 = 12 tests)

- [ ] **Step 8: Verify full build**

Run: `go build ./... && go vet ./...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/gateway/gateway.go internal/gateway/gateway_test.go main.go
git commit -m "feat(gateway): 网关集成来源信任检查"
```

---

### Task 3: Config fields for trust control

**Covers:** S4 (Configuration), S6 (Config tests)

**Files:**
- Modify: `internal/config/config.go` — Config struct, configFields, fieldName constants, defaultsSource, fileSource (yamlFields), envSource, flagSource (flagValues + parseFlags), Validate, sampleConfig, new `parseCIDRList` func, new `Warnings` method
- Modify: `internal/config/config_test.go` — add trust config tests

**Interfaces:**
- Consumes: nothing from other tasks (config is standalone)
- Produces: `Config.TrustedNetworks []string`, `Config.UntrustedProxyAction string`, `Config.Warnings() []string`

- [ ] **Step 1: Write failing config tests**

Add to `internal/config/config_test.go`:

```go
// --- trust control config ---

func TestValidateBadUntrustedProxyAction(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "bogus"}
	if err := c.Validate(); err == nil {
		t.Fatal("bad untrusted-proxy-action should fail validation")
	}
}

func TestValidateBadTrustedNetworkCIDR(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject", TrustedNetworks: []string{"not-a-cidr"}}
	if err := c.Validate(); err == nil {
		t.Fatal("invalid CIDR should fail validation")
	}
}

func TestValidateTrustDefaults(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, LogConsoleLevel: "info", LogConsoleFormat: "text"}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	if c.UntrustedProxyAction != "reject" {
		t.Fatalf("untrusted-proxy-action default: want reject, got %q", c.UntrustedProxyAction)
	}
}

func TestParseCIDRListTrimSpace(t *testing.T) {
	got := parseCIDRList("10.0.0.0/8, 192.168.1.0/24")
	want := []string{"10.0.0.0/8", "192.168.1.0/24"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseCIDRList: want %v, got %v", want, got)
	}
}

func TestParseCIDRListEmptyValues(t *testing.T) {
	got := parseCIDRList("")
	if len(got) != 0 {
		t.Fatalf("empty string should produce empty list, got %v", got)
	}
	got = parseCIDRList("10.0.0.0/8,")
	if len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("trailing comma: want [10.0.0.0/8], got %v", got)
	}
}

func TestEnvTrustedNetworks(t *testing.T) {
	t.Setenv("PROXYDGE_TRUSTED_NETWORKS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("PROXYDGE_UNTRUSTED_PROXY_ACTION", "strip")
	var c Config
	if err := (envSource{}).Apply(&c); err != nil {
		t.Fatalf("env: %v", err)
	}
	if len(c.TrustedNetworks) != 2 || c.TrustedNetworks[0] != "10.0.0.0/8" || c.TrustedNetworks[1] != "192.168.0.0/16" {
		t.Fatalf("trusted-networks: got %v", c.TrustedNetworks)
	}
	if c.UntrustedProxyAction != "strip" {
		t.Fatalf("untrusted-proxy-action: want strip, got %q", c.UntrustedProxyAction)
	}
}

func TestWarningsEmptyTrustedNetworks(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject"}
	ws := c.Warnings()
	if len(ws) != 1 {
		t.Fatalf("empty trusted-networks: want 1 warning, got %d", len(ws))
	}
}

func TestWarningsStripAction(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "strip", TrustedNetworks: []string{"10.0.0.0/8"}}
	ws := c.Warnings()
	if len(ws) != 1 {
		t.Fatalf("strip action: want 1 warning, got %d", len(ws))
	}
}

func TestWarningsBoth(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "strip"}
	ws := c.Warnings()
	if len(ws) != 2 {
		t.Fatalf("both: want 2 warnings, got %d", len(ws))
	}
}

func TestWarningsNone(t *testing.T) {
	c := Config{Upstream: "1.2.3.4:80", Policy: "use", DetectTimeout: time.Second, UntrustedProxyAction: "reject", TrustedNetworks: []string{"10.0.0.0/8"}}
	ws := c.Warnings()
	if len(ws) != 0 {
		t.Fatalf("secure config: want 0 warnings, got %d: %v", len(ws), ws)
	}
}
```

Also add to `internal/config/provenance_test.go`:

```go
func TestProvenanceTrustFields(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.yaml", "upstream: 1.2.3.4:80\ntrusted-networks:\n  - 10.0.0.0/8\nuntrusted-proxy-action: strip\n")
	t.Setenv("PROXYDGE_UNTRUSTED_PROXY_ACTION", "reject") // env beats file
	c, err := Load([]string{"-config", p, "-trusted-networks", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.sourceOf(fTrustedNetworks) != "flag" {
		t.Fatalf("trusted-networks source: want flag, got %q", c.sourceOf(fTrustedNetworks))
	}
	if c.sourceOf(fUntrustedProxyAction) != "env" {
		t.Fatalf("untrusted-proxy-action source: want env, got %q", c.sourceOf(fUntrustedProxyAction))
	}
}
```

And add trust fields to `TestDescribeContainsSources` assertions (insert before the `listen` check):

```go
	if !strings.Contains(desc, "trusted-networks = [192.168.0.0/16] (flag)") {
		t.Fatalf("describe missing trusted-networks provenance:\n%s", desc)
	}
	if !strings.Contains(desc, "untrusted-proxy-action = reject (env)") {
		t.Fatalf("describe missing untrusted-proxy-action provenance:\n%s", desc)
	}
```

For this to work, update the test's setup to include trust config:
```go
	t.Setenv("PROXYDGE_UNTRUSTED_PROXY_ACTION", "reject")
	// ... in the Load args:
	c, err := Load([]string{"-config", p, "-log-file", "/tmp/x.log", "-trusted-networks", "192.168.0.0/16"})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestValidate -count=1 && go test ./internal/config/ -run TestParseCIDR -count=1 && go test ./internal/config/ -run TestEnvTrusted -count=1 && go test ./internal/config/ -run TestWarnings -count=1`
Expected: FAIL — `UntrustedProxyAction` field, `parseCIDRList` func, `Warnings` method not defined

- [ ] **Step 3: Implement config changes**

In `internal/config/config.go`:

1. Add `"net"` to imports (after `"strings"`).

2. Add two fields to `Config` struct (after `DetectTimeout`):

```go
	TrustedNetworks      []string
	UntrustedProxyAction  string
```

3. Add two entries to `configFields` (after `detect-timeout`):

```go
	{"trusted-networks", func(c *Config) any { return c.TrustedNetworks }},
	{"untrusted-proxy-action", func(c *Config) any { return c.UntrustedProxyAction }},
```

4. Add two constants to the `fieldName` block:

```go
	fTrustedNetworks      = "trusted-networks"
	fUntrustedProxyAction = "untrusted-proxy-action"
```

5. Add `parseCIDRList` function (after `validFormat`):

```go
// parseCIDRList splits a comma-separated CIDR string, trims whitespace
// from each entry, and skips empty strings. Used by envSource and flagSource
// for the -trusted-networks value.
func parseCIDRList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

6. In `defaultsSource.Apply`, add after `c.LogFileFormat = "text"`:

```go
	c.UntrustedProxyAction = "reject"
	// TrustedNetworks defaults to nil (trust everyone).
```

7. Add two fields to `yamlFields` struct:

```go
	TrustedNetworks      []string `yaml:"trusted-networks"`
	UntrustedProxyAction *string  `yaml:"untrusted-proxy-action"`
```

8. In `fileSource.Apply`, add before the `log` section handling:

```go
	if y.TrustedNetworks != nil {
		c.TrustedNetworks = y.TrustedNetworks
		c.mark(fTrustedNetworks, src)
	}
	if y.UntrustedProxyAction != nil {
		c.UntrustedProxyAction = *y.UntrustedProxyAction
		c.mark(fUntrustedProxyAction, src)
	}
```

9. In `envSource.Apply`, add before `return nil`:

```go
	if v, ok := os.LookupEnv(envPrefix + "TRUSTED_NETWORKS"); ok && v != "" {
		c.TrustedNetworks = parseCIDRList(v)
		c.mark(fTrustedNetworks, "env")
	}
	if v, ok := os.LookupEnv(envPrefix + "UNTRUSTED_PROXY_ACTION"); ok && v != "" {
		c.UntrustedProxyAction = v
		c.mark(fUntrustedProxyAction, "env")
	}
```

10. Add two fields to `flagValues` struct:

```go
	trustedNetworks      *string
	untrustedProxyAction *string
```

11. In `parseFlags`, add two flags:

```go
	fv.trustedNetworks = fs.String("trusted-networks", "", "trusted networks (comma-separated CIDRs, empty=all)")
	fv.untrustedProxyAction = fs.String("untrusted-proxy-action", "", "action for untrusted sources with PROXY header: reject|strip")
```

12. In `flagSource.Apply`, add before `return nil`:

```go
	if s.set["trusted-networks"] {
		c.TrustedNetworks = parseCIDRList(*s.fv.trustedNetworks)
		c.mark(fTrustedNetworks, "flag")
	}
	if s.set["untrusted-proxy-action"] {
		c.UntrustedProxyAction = *s.fv.untrustedProxyAction
		c.mark(fUntrustedProxyAction, "flag")
	}
```

13. In `Validate`, add after the policy check:

```go
	switch c.UntrustedProxyAction {
	case "reject", "strip":
	default:
		return fmt.Errorf("config: invalid untrusted-proxy-action %q (reject|strip)", c.UntrustedProxyAction)
	}
	for _, cidr := range c.TrustedNetworks {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: invalid trusted-networks entry %q: %w", cidr, err)
		}
	}
```

14. Add `Warnings` method (after `sourceOf`):

```go
// Warnings returns security warnings for the startup banner. An empty
// trusted-networks and untrusted-proxy-action=strip both produce warnings
// explaining the consequences.
func (c *Config) Warnings() []string {
	var ws []string
	if len(c.TrustedNetworks) == 0 {
		ws = append(ws, "trusted-networks is empty: all sources are trusted. "+
			"Any IP can spoof source addresses via PROXY headers. "+
			"Configure trusted-networks in production.")
	}
	if c.UntrustedProxyAction == "strip" {
		ws = append(ws, "untrusted-proxy-action=strip: non-trusted sources with PROXY headers "+
			"will have their headers stripped and forwarded with real socket addresses. "+
			"They can still connect — use reject (default) to deny them.")
	}
	return ws
}
```

15. Update `sampleConfig` — insert after `detect-timeout` line and before `log:`:

```
# Trust control: only these networks may send PROXY headers.
# Empty (default) trusts everyone — configure in production to prevent spoofing.
trusted-networks:
  # - "10.0.0.0/8"
  # - "192.168.1.0/24"
untrusted-proxy-action: "reject"   # reject (default) | strip

```

- [ ] **Step 4: Run config tests**

Run: `go test ./internal/config/ -count=1 -v`
Expected: PASS (existing + new tests)

- [ ] **Step 5: Verify full build**

Run: `go build ./... && go vet ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/provenance_test.go
git commit -m "feat(config): 新增来源信任控制配置字段与安全警告"
```

---

### Task 4: main.go full wiring

**Covers:** S5 (main.go), S6 (main tests)

**Files:**
- Modify: `main.go` — TrustChecker construction, injection, warnings print, `untrustedProxyAction` helper, help text
- Modify: `main_test.go` — accept/invalid flag tests

**Interfaces:**
- Consumes: `gateway.NewTrustChecker`, `gateway.UntrustedAction` from Task 1; `Config.TrustedNetworks`, `Config.UntrustedProxyAction`, `Config.Warnings` from Task 3
- Produces: Fully wired gateway with trust control from config

- [ ] **Step 1: Write failing main tests**

Add to `main_test.go`:

```go
func TestStartInvalidUntrustedProxyAction(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-untrusted-proxy-action", "bogus", "-listen", "bad-listen"}); code != 2 {
		t.Fatalf("invalid -untrusted-proxy-action: want exit 2, got %d", code)
	}
}

func TestStartValidUntrustedProxyAction(t *testing.T) {
	// Valid flag reaches listen/serve; bad listen forces runtime exit 1.
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-untrusted-proxy-action", "strip", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("valid -untrusted-proxy-action=strip: want exit 1 (runtime), got %d", code)
	}
}

func TestStartValidTrustedNetworks(t *testing.T) {
	if code := run([]string{"start", "-upstream", "127.0.0.1:1", "-trusted-networks", "10.0.0.0/8", "-listen", "bad-listen"}); code != 1 {
		t.Fatalf("valid -trusted-networks: want exit 1 (runtime), got %d", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run TestStart -count=1`
Expected: `TestStartValidUntrustedProxyAction` and `TestStartValidTrustedNetworks` FAIL — the valid flags should reach listen (exit 1) but may exit 2 if the config doesn't pass validation (because `UntrustedProxyAction` defaults to "reject" and is valid). Actually these should pass already since Task 3 added the config validation. Let me check...

Actually, these tests should PASS after Task 3 because the config accepts the flags and validation passes, reaching listen (bad-listen → exit 1). So these are verification tests, not failing-first tests. Run them:

Run: `go test ./ -run TestStartValid -count=1`
Expected: PASS (config from Task 3 already handles validation)

Run: `go test ./ -run TestStartInvalidUntrusted -count=1`
Expected: PASS (config validation rejects "bogus")

If they pass already, skip to Step 3. If they fail, fix the issue.

- [ ] **Step 3: Wire TrustChecker into main.go**

In `main.go`, update `cmdStart`:

1. After `cfg` is loaded and the banner is printed, add warnings:

```go
	fmt.Fprintln(os.Stderr, version.String())
	fmt.Fprint(os.Stderr, cfg.Describe())
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
```

2. After logger construction, before `gateway.New`, add TrustChecker construction:

```go
	trust, err := gateway.NewTrustChecker(cfg.TrustedNetworks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		return 1
	}
```

3. Update the `gateway.New` call to pass real trust and untrusted:

```go
	g := gateway.New(
		ln, transport.TCPDialer{},
		goproxyproto.NewReader(), goproxyproto.NewWriter(),
		gatewayPolicy(cfg.Policy), cfg.Upstream, cfg.DetectTimeout, logger,
		trust, untrustedProxyAction(cfg.UntrustedProxyAction),
	)
```

4. Add the `untrustedProxyAction` helper (after `gatewayPolicy`):

```go
// untrustedProxyAction maps the validated config string to the gateway's
// enum. It is in main (not the config package) so the gateway stays free
// of config imports.
func untrustedProxyAction(s string) gateway.UntrustedAction {
	if s == "strip" {
		return gateway.UntrustedStrip
	}
	return gateway.UntrustedReject
}
```

5. Update the help text — add after `-detect-timeout` line:

```
  -trusted-networks <cidrs>      trusted networks (comma-separated CIDRs, empty=all)
  -untrusted-proxy-action <a>    reject|strip (default "reject")
```

- [ ] **Step 4: Run all tests**

Run: `go test ./... -count=1`
Expected: PASS (all packages)

- [ ] **Step 5: Verify full build and vet**

Run: `go build ./... && go vet ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(main): 接线信任检查器与启动安全警告"
```

---

## Self-Review Checklist (for the implementer to verify)

After all 4 tasks:

- [ ] `go build ./...` — PASS
- [ ] `go vet ./...` — PASS
- [ ] `go test ./... -count=1` — ALL PASS
- [ ] Spec S3 pipeline: trust check sits after reader.Read, before policy ✓
- [ ] Spec S3 policy timing note: policy evaluates normalized source after trust ✓
- [ ] Spec S4: all three sources (file/env/flag) handle both new fields ✓
- [ ] Spec S4: CSV parsing trims whitespace + skips empty ✓
- [ ] Spec S4: startup warnings for empty trusted-networks and strip ✓
- [ ] Spec S5: TrustChecker nil = trust everyone ✓
- [ ] Spec S5: remoteIP from socket RemoteAddr, never from PROXY header SrcIP ✓
- [ ] Spec S6: all test cases implemented ✓
