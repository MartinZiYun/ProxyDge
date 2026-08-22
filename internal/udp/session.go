// Package udp implements the UDP PROXY Protocol gateway. Unlike the TCP
// gateway (which uses stream-based pipeStream), UDP has its own datagram/
// session model: per-session connected upstream sockets, idle-timeout-based
// lifecycle, and datagram-level PROXY header encoding.
package udp

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"proxydge/internal/proxyproto"
)

// sessionKey uniquely identifies a UDP client session. Includes IPv6 zone
// to distinguish link-local addresses on different interfaces.
type sessionKey struct {
	ip   [16]byte // net.IP.To16() — IPv4-in-IPv6 form
	port uint16
	zone string // IPv6 zone (e.g., "eth0") — "" for IPv4/global
}

// keyFromUDPAddr canonicalizes a *net.UDPAddr into a sessionKey. All key
// construction goes through this helper — no ad-hoc key creation.
func keyFromUDPAddr(addr *net.UDPAddr) sessionKey {
	var ip [16]byte
	if addr.IP != nil {
		copy(ip[:], addr.IP.To16())
	}
	return sessionKey{ip: ip, port: uint16(addr.Port), zone: addr.Zone}
}

// cloneHeader deep-copies a proxyproto.Header so persisted session metadata
// owns its address byte slices. The ReadFromUDP packet buffer is reused for
// the next datagram — retained references would silently corrupt.
// TLVs are not populated or forwarded in the initial design (set to nil).
func cloneHeader(h proxyproto.Header) proxyproto.Header {
	return proxyproto.Header{
		SrcIP:   append([]byte(nil), h.SrcIP...),
		DstIP:   append([]byte(nil), h.DstIP...),
		SrcPort: h.SrcPort,
		DstPort: h.DstPort,
		Family:  h.Family,
		TLVs:    nil,
	}
}

// UDPSession manages one client's routing, upstream socket, and lifecycle.
// It does NOT carry protocol state — headerSent and inputSource are
// mode-specific implementation details, not protocol concepts.
type UDPSession struct {
	key         sessionKey
	clientAddr  *net.UDPAddr // for WriteToUDP responses
	listener    *net.UDPConn // shared listener (for WriteToUDP to client)
	upstream    *net.UDPConn // per-session connected socket
	idleTimeout time.Duration
	idleTimer   *time.Timer
	done        chan struct{}
	once        sync.Once
	log         *slog.Logger
	onExpire    func(sessionKey) // called once when session expires
	wg          *sync.WaitGroup  // tracks reader goroutine for graceful shutdown

	// first_datagram OUTPUT state (only when outputMode == FirstDatagram)
	headerSent atomic.Bool

	// Input flow state: when a PROXY header is received on the first datagram
	// from a source and subsequent datagrams are headerless (auto-detected at
	// runtime — not a config mode), the parsed Header/Source is stored here so
	// headerless datagrams can recover the original source.
	// Persisted ONLY after trust check passes. Deep-copied via cloneHeader.
	inputSource  atomic.Pointer[proxyproto.Header]
	inputSrcKind atomic.Pointer[proxyproto.Source]
}

// newSession creates a UDPSession, starts the idle timer and reader goroutine.
// The caller is responsible for adding the session to the manager's map.
func newSession(
	key sessionKey,
	clientAddr *net.UDPAddr,
	listener *net.UDPConn,
	upstream *net.UDPConn,
	idleTimeout time.Duration,
	log *slog.Logger,
	onExpire func(sessionKey),
) *UDPSession {
	s := &UDPSession{
		key:         key,
		clientAddr:  clientAddr,
		listener:    listener,
		upstream:    upstream,
		idleTimeout: idleTimeout,
		done:        make(chan struct{}),
		log:         log,
		onExpire:    onExpire,
	}
	s.idleTimer = time.AfterFunc(idleTimeout, s.expire)
	return s
}

// startReader launches the upstream→client reader goroutine. Called by the
// manager ONLY after LoadOrStore succeeds — the losing session in a race
// must not start a reader (its upstream socket is closed by the manager).
func (s *UDPSession) startReader(wg *sync.WaitGroup) {
	s.wg = wg
	wg.Add(1)
	go s.readLoop()
}

// refresh resets the idle timer. Called on each datagram (either direction).
func (s *UDPSession) refresh() {
	s.idleTimer.Reset(s.idleTimeout)
}

// expire closes the session: clears state, closes upstream socket, signals
// reader goroutine, and removes from manager. Idempotent via sync.Once.
func (s *UDPSession) expire() {
	s.once.Do(func() {
		close(s.done)             // signal reader goroutine
		s.idleTimer.Stop()        // stop timer (may have already fired)
		s.inputSource.Store(nil)  // clear input flow state (security)
		s.inputSrcKind.Store(nil) // prevent source port reuse leakage
		_ = s.upstream.Close()    // unblocks reader's Read()
		if s.onExpire != nil {
			s.onExpire(s.key) // remove from manager map
		}
	})
}

// readLoop is the per-session reader goroutine. It reads response datagrams
// from the upstream socket and forwards them to the client via the listener.
// Exits when the upstream socket is closed (by expire or read error).
func (s *UDPSession) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, err := s.upstream.Read(buf)
		if err != nil {
			select {
			case <-s.done:
				// Session expired — clean exit
			default:
				// Read error (downstream gone) — trigger expiry
				s.log.Debug("upstream read error, expiring session", "remote", s.clientAddr, "err", err)
				s.expire()
			}
			return
		}
		select {
		case <-s.done:
			return // Session expired — don't forward
		default:
		}
		_, _ = s.listener.WriteToUDP(buf[:n], s.clientAddr)
		s.refresh()
	}
}
