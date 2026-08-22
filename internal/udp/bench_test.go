package udp

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"proxydge/internal/gateway"
)

func startBenchDownstream(b *testing.B) string {
	b.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		b.Fatalf("downstream listen: %v", err)
	}
	b.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, peer, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(buf[:n], peer)
		}
	}()
	return pc.LocalAddr().String()
}

func startBenchGateway(b *testing.B, downstream string, outputMode OutputMode) string {
	b.Helper()
	g, err := New(
		"127.0.0.1:0", downstream,
		gateway.PolicyUse, nil, gateway.UntrustedReject,
		outputMode, 30*time.Second, 1024, 65535,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		b.Fatalf("gateway: %v", err)
	}
	b.Cleanup(func() { g.Close() })
	go func() { _ = g.Serve() }()
	time.Sleep(50 * time.Millisecond)
	return g.listener.LocalAddr().String()
}

// BenchmarkUDPDatagramThroughput measures single-socket datagram round-trip
// throughput: client sends a datagram, gateway normalizes + forwards to
// downstream echo, gateway forwards echo back to client.
func BenchmarkUDPDatagramThroughput(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr, OutputEveryDatagram)

	payload := []byte("PING1234567890") // 16 bytes
	pc, err := net.Dial("udp", gwAddr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer pc.Close()
	buf := make([]byte, 65535)

	// Warm up — establish session
	_, _ = pc.Write(payload)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = pc.Read(buf)

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := pc.Write(payload); err != nil {
			b.Fatalf("write: %v", err)
		}
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			b.Fatalf("read: %v", err)
		}
		if n != len(payload) {
			b.Fatalf("echo size: want %d, got %d", len(payload), n)
		}
	}
}

// BenchmarkUDPDatagramConcurrent measures throughput under concurrent senders.
func BenchmarkUDPDatagramConcurrent(b *testing.B) {
	downAddr := startBenchDownstream(b)
	gwAddr := startBenchGateway(b, downAddr, OutputEveryDatagram)

	payload := []byte("PING1234567890")
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		pc, err := net.Dial("udp", gwAddr)
		if err != nil {
			b.Fatalf("dial: %v", err)
		}
		defer pc.Close()
		buf := make([]byte, 65535)
		for pb.Next() {
			_, _ = pc.Write(payload)
			pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = pc.Read(buf)
		}
	})
}
