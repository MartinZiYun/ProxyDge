package gateway

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"proxydge/internal/proxyproto"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/tcp"
)

const tcp4HeaderHex = "0d0a0d0a000d0a515549540a2111000cc0000201c633640104d21f90"

var sigV2 = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return b
}

// startDownstream runs an echo+recorder TCP server. For each connection it
// reads everything until EOF (i.e. until the gateway half-closes), records the
// bytes, then echoes them back so the client observes a response.
func startDownstream(t *testing.T) (addr string, recorded chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("downstream listen: %v", err)
	}
	recorded = make(chan []byte, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf, _ := io.ReadAll(c)
				recorded <- buf
				_, _ = c.Write(buf) // echo
				_ = c.Close()
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), recorded
}

// startGateway runs a gateway over real TCP with the production adapters.
func startGateway(t *testing.T, policy Policy, upstream string) string {
	t.Helper()
	ln, err := tcp.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gateway listen: %v", err)
	}
	g := New(ln, tcp.TCPDialer{}, goproxyproto.NewReader(), goproxyproto.NewWriter(), policy, upstream, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, UntrustedReject)
	go func() { _ = g.Serve() }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// startGatewayTrusted runs a gateway with a non-nil TrustChecker so trust
// decisions are exercised. The client connects from 127.0.0.1, so a trust
// list of "10.0.0.0/8" makes the client untrusted, while "127.0.0.0/8" makes
// it trusted.
func startGatewayTrusted(t *testing.T, policy Policy, upstream string, trustCIDRs []string, untrusted UntrustedAction) string {
	t.Helper()
	ln, err := tcp.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gateway listen: %v", err)
	}
	tc, err := NewTrustChecker(trustCIDRs)
	if err != nil {
		t.Fatalf("NewTrustChecker: %v", err)
	}
	g := New(ln, tcp.TCPDialer{}, goproxyproto.NewReader(), goproxyproto.NewWriter(), policy, upstream, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), tc, untrusted)
	go func() { _ = g.Serve() }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// dialAndExchange connects to the gateway, writes payload (after an optional
// pre-header), half-closes the write side, and reads the full echo back.
func dialAndExchange(t *testing.T, gatewayAddr string, preHeader, payload []byte) (echo []byte, clientLocalPort int) {
	t.Helper()
	c, err := net.Dial("tcp", gatewayAddr)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer c.Close()
	if len(preHeader) > 0 {
		if _, err := c.Write(preHeader); err != nil {
			t.Fatalf("write header: %v", err)
		}
	}
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		clientLocalPort = tcp.LocalAddr().(*net.TCPAddr).Port
	}
	echo, _ = io.ReadAll(c)
	return echo, clientLocalPort
}

func TestGatewayUseV1(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	gw := startGateway(t, PolicyUse, downAddr)

	v1Header := []byte("PROXY TCP4 192.0.2.1 198.51.100.1 1234 8080\r\n")
	echo, _ := dialAndExchange(t, gw, v1Header, []byte("PING"))

	select {
	case got := <-recorded:
		want := append(mustHex(t, tcp4HeaderHex), []byte("PING")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("downstream received:\nwant %x\n got %x", want, got)
		}
		if !bytes.Equal(echo, want) {
			t.Fatalf("client echo:\nwant %x\n got %x", want, echo)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayUseV2(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	gw := startGateway(t, PolicyUse, downAddr)

	v2Header := mustHex(t, tcp4HeaderHex)
	echo, _ := dialAndExchange(t, gw, v2Header, []byte("PING"))

	select {
	case got := <-recorded:
		want := append(mustHex(t, tcp4HeaderHex), []byte("PING")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("downstream received:\nwant %x\n got %x", want, got)
		}
		if !bytes.Equal(echo, want) {
			t.Fatalf("client echo:\nwant %x\n got %x", want, echo)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayUseDirect(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	gw := startGateway(t, PolicyUse, downAddr)

	echo, clientPort := dialAndExchange(t, gw, nil, []byte("PING"))

	select {
	case got := <-recorded:
		if !bytes.HasPrefix(got, sigV2) {
			t.Fatalf("downstream did not receive v2 header; got %x", got)
		}
		if !bytes.HasSuffix(got, []byte("PING")) {
			t.Fatalf("downstream missing payload; got %x", got)
		}
		// Parse the emitted v2 header and verify the source port is the real
		// client port (HeaderFromConn used the socket's RemoteAddr).
		h, src, err := goproxyproto.NewReader().Read(bufio.NewReader(bytes.NewReader(got)))
		if err != nil || src != proxyproto.SourceV2 {
			t.Fatalf("parse emitted header: src=%v err=%v", src, err)
		}
		if h.SrcPort != uint16(clientPort) {
			t.Fatalf("src port: want %d (client local), got %d", clientPort, h.SrcPort)
		}
		if !h.SrcIP.Equal(net.IPv4(127, 0, 0, 1)) {
			t.Fatalf("src ip: want 127.0.0.1, got %s", h.SrcIP)
		}
		if !bytes.Equal(echo, got) {
			t.Fatalf("echo != recorded: echo %x recorded %x", echo, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("downstream did not receive within timeout")
	}
}

func TestGatewayRequireRejectsDirect(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	gw := startGateway(t, PolicyRequire, downAddr)

	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("PING")) // no PROXY header → policy=require must reject
	buf, _ := io.ReadAll(c)        // gateway closed the connection → EOF

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

func TestGatewayRejectRejectsHeader(t *testing.T) {
	downAddr, recorded := startDownstream(t)
	gw := startGateway(t, PolicyReject, downAddr)

	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Send a valid v2 header → policy=reject must reject.
	_, _ = c.Write(mustHex(t, tcp4HeaderHex))
	buf, _ := io.ReadAll(c)

	select {
	case got := <-recorded:
		t.Fatalf("downstream unexpectedly received %x", got)
	case <-time.After(300 * time.Millisecond):
	}
	if len(buf) != 0 {
		t.Fatalf("expected EOF (gateway closed), got %q", buf)
	}
}

// TestGatewayUsePartialCandidateTimeout covers the user's core concern: a
// direct client that sends a short payload starting with a PROXY candidate
// byte ('P') but never sends a complete header, then waits for a response.
// Without a detection deadline the gateway would block forever on Peek(5).
// With detectTimeout it must treat the connection as direct and forward the
// (buffered, peeked) bytes downstream within the timeout — no deadlock, no
// data loss. We assert via a streaming echo downstream (echo-while-reading),
// which reflects bytes back without waiting for EOF.
func TestGatewayUsePartialCandidateTimeout(t *testing.T) {
	// Streaming echo: each read chunk is written back immediately, so the
	// client gets a response without the downstream needing a FIN.
	downLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("downstream listen: %v", err)
	}
	recorded := make(chan []byte, 8)
	go func() {
		for {
			c, err := downLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				var all bytes.Buffer
				buf := make([]byte, 256)
				for {
					n, rerr := c.Read(buf)
					if n > 0 {
						all.Write(buf[:n])
						_, _ = c.Write(buf[:n]) // echo immediately
					}
					if rerr != nil {
						recorded <- all.Bytes()
						_ = c.Close()
						return
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() { _ = downLn.Close() })

	gw := startGateway(t, PolicyUse, downLn.Addr().String())

	c, err := net.Dial("tcp", gw)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// "PING" starts with 'P' (v1 candidate) but only 4 bytes; client does not
	// CloseWrite and waits for a response — Peek(5) would block forever without
	// the detection deadline.
	_, _ = c.Write([]byte("PING"))

	// Expect the downstream to reflect the v2 header + "PING" back promptly.
	// The gateway writes the header (WriteTo) then the payload (io.Copy), which
	// may arrive as separate chunks; accumulate reads until we have both.
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var echo []byte
	tmp := make([]byte, 256)
	for len(echo) < 32 { // 28-byte v2 header + 4-byte "PING"
		n, err := c.Read(tmp)
		echo = append(echo, tmp[:n]...)
		if err != nil {
			if len(echo) >= 32 {
				break
			}
			t.Fatalf("read echo (deadlock not resolved?): %v (got %x)", err, echo)
		}
	}
	if !bytes.HasPrefix(echo, sigV2) || !bytes.HasSuffix(echo, []byte("PING")) {
		t.Fatalf("echo: want <v2 header>...PING, got %x", echo)
	}
}

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

	// v2 header with SrcIP=10.0.0.1 (claims to be from trusted network)
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
