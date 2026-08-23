package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/tcp"
)

// --- helpers (benchmark-specific, accept *testing.B) ---

// startBenchDownstream starts a read-all-then-echo TCP server. Each connection
// reads until EOF, then echoes everything back and closes. Returns the listen
// address.
func startBenchDownstream(b *testing.B) string {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("downstream listen: %v", err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf, _ := io.ReadAll(c)
				_, _ = c.Write(buf)
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().String()
}

// startBenchGateway starts a gateway with PolicyUse (direct connections),
// 50ms detect timeout, and a discarding logger.
func startBenchGateway(b *testing.B, upstream string) string {
	b.Helper()
	ln, err := tcp.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("gateway listen: %v", err)
	}
	g := New(ln, tcp.TCPDialer{}, goproxyproto.NewReader(), goproxyproto.NewWriter(2),
		PolicyUse, upstream, 50*time.Millisecond, 0,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, UntrustedReject, FamilyMismatchReject)
	go func() { _ = g.Serve() }()
	b.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// dialSendReceive connects, sends payload, half-closes, reads the echo, then
// closes. SetLinger(0) avoids Windows TIME_WAIT port exhaustion under high
// connection churn.
func dialSendReceive(b *testing.B, gwAddr string, payload []byte) {
	b.Helper()
	c, err := net.Dial("tcp", gwAddr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	if _, err := c.Write(payload); err != nil {
		b.Fatalf("write: %v", err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	if _, err := io.Copy(io.Discard, c); err != nil {
		b.Fatalf("read echo: %v", err)
	}
	_ = c.Close()
}

// --- benchmarks ---

// BenchmarkGatewaySingleConnThroughput measures single-connection pipe
// throughput: client sends 256KB, gateway pipes to downstream, downstream
// echoes, gateway pipes back. b.SetBytes reports MB/s based on payload size.
func BenchmarkGatewaySingleConnThroughput(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr)

	payload := bytes.Repeat([]byte("x"), 256*1024)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		dialSendReceive(b, gwAddr, payload)
	}
}

// BenchmarkGatewayConcurrentThroughput measures total throughput under
// concurrent connections. Each goroutine sends a 16KB payload through the
// gateway. RunParallel distributes b.N iterations across GOMAXPROCS goroutines.
func BenchmarkGatewayConcurrentThroughput(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr)

	payload := bytes.Repeat([]byte("x"), 16*1024)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			dialSendReceive(b, gwAddr, payload)
		}
	})
}

// BenchmarkGatewayLatency measures per-connection latency with a tiny payload
// (4 bytes). Each iteration is a full connection lifecycle: TCP handshake,
// PROXY header detection, downstream dial, PROXY v2 header write, 4-byte
// round-trip, teardown. Reports ns/op and allocs/op.
func BenchmarkGatewayLatency(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr)

	payload := []byte("PING")
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		dialSendReceive(b, gwAddr, payload)
	}
}

// BenchmarkGatewayConcurrentLatency measures latency under concurrent load.
// Each goroutine does a 4-byte round-trip through the gateway.
func BenchmarkGatewayConcurrentLatency(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr)

	payload := []byte("PING")
	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			dialSendReceive(b, gwAddr, payload)
		}
	})
}
