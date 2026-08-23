package udp

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

// Tests for unlimited-mode (maxSessions<=0) bookkeeping: the capacity checks
// are skipped, so the only invariant left is that the counter stays balanced
// across concurrent Creates — including the LoadOrStore race-lost rollback
// path, which gateway-level tests cannot reach because Serve serializes
// handleDatagram.

func unlimitedTestHarness(t *testing.T) (*UDPSessionManager, *net.UDPConn) {
	t.Helper()
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return NewUDPSessionManager(0), l
}

// TestUnlimitedSameKeyConcurrentCreateBalanceCount: N goroutines race to
// create the SAME session key under an unlimited cap. Exactly one wins the
// LoadOrStore; every loser rolls its increment back (the manager closes a
// race-loser's upstream itself). All callers must receive a usable session
// and the counter must settle at exactly 1.
//
// OWNERSHIP: once handed to Create, the upstream belongs to the
// manager/session — the caller must NOT close it. Closing the winner's
// upstream here would trip its readLoop into expire() and remove the session,
// masquerading as counter drift.
func TestUnlimitedSameKeyConcurrentCreateBalanceCount(t *testing.T) {
	m, l := unlimitedTestHarness(t)

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}
	key := keyFromUDPAddr(addr)

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			up, err := net.DialUDP("udp", nil, l.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Errorf("create %d upstream: %v", i, err)
				return
			}
			// No defer up.Close(): ownership transfers to Create.
			if _, err := m.Create(key, addr, l, up, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
				t.Errorf("create %d: unexpected error under unlimited cap: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := m.Count(); got != 1 {
		t.Fatalf("counter drift after same-key race: want 1, got %d", got)
	}
	m.ExpireAll()
	if got := m.Count(); got != 0 {
		t.Fatalf("count after ExpireAll: want 0, got %d", got)
	}
}

// TestUnlimitedDistinctKeysAllCreated: N distinct sources under an unlimited
// cap all create successfully and the counter lands exactly on N.
func TestUnlimitedDistinctKeysAllCreated(t *testing.T) {
	m, l := unlimitedTestHarness(t)

	const n = 64
	for i := 0; i < n; i++ {
		client := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000 + i}
		up, err := net.DialUDP("udp", nil, l.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatalf("upstream %d: %v", i, err)
		}
		if _, err := m.Create(keyFromUDPAddr(client), client, l, up, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if got := m.Count(); got != n {
		t.Fatalf("count: want %d, got %d", n, got)
	}
	m.ExpireAll()
	if got := m.Count(); got != 0 {
		t.Fatalf("count after ExpireAll: want 0, got %d", got)
	}
}
