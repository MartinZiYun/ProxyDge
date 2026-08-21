package udp

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"proxydge/internal/proxyproto"
)

// ErrMaxSessions is returned when the session manager is at capacity.
var ErrMaxSessions = errors.New("udp: max sessions reached")

// UDPSessionManager tracks all active sessions, enforces max session count,
// and handles session creation/removal. Session removal happens inside
// expire()'s sync.Once block — no window for state inheritance.
type UDPSessionManager struct {
	sessions    sync.Map // sessionKey → *UDPSession
	count       atomic.Int64
	maxSessions int64
}

// NewUDPSessionManager creates a manager with the given max session limit.
func NewUDPSessionManager(maxSessions int64) *UDPSessionManager {
	return &UDPSessionManager{maxSessions: maxSessions}
}

// Load returns an existing session by key, or nil if not found.
func (m *UDPSessionManager) Load(key sessionKey) (*UDPSession, bool) {
	v, ok := m.sessions.Load(key)
	if !ok {
		return nil, false
	}
	return v.(*UDPSession), true
}

// Create creates a new session. Returns ErrMaxSessions if at capacity.
// Uses LoadOrStore for atomic creation — if another goroutine won the race,
// returns the existing session and rolls back the count.
func (m *UDPSessionManager) Create(
	key sessionKey,
	clientAddr *net.UDPAddr,
	listener *net.UDPConn,
	upstream *net.UDPConn,
	idleTimeout time.Duration,
	log *slog.Logger,
) (*UDPSession, error) {
	if m.count.Load() >= m.maxSessions {
		return nil, ErrMaxSessions
	}
	// Atomic check-and-increment
	newCount := m.count.Add(1)
	if newCount > m.maxSessions {
		m.count.Add(-1) // rollback
		return nil, ErrMaxSessions
	}
	s := newSession(key, clientAddr, listener, upstream, idleTimeout, log, m.remove)
	actual, loaded := m.sessions.LoadOrStore(key, s)
	if loaded {
		// Another goroutine won the race; rollback our count and return existing
		m.count.Add(-1)
		return actual.(*UDPSession), nil
	}
	return s, nil
}

// remove deletes a session from the map and decrements the count.
// Called from session.expire() inside sync.Once — atomic with state clearing.
func (m *UDPSessionManager) remove(key sessionKey) {
	m.sessions.Delete(key)
	m.count.Add(-1)
}

// ExpireAll expires all active sessions (for graceful shutdown).
func (m *UDPSessionManager) ExpireAll() {
	m.sessions.Range(func(_, v any) bool {
		v.(*UDPSession).expire()
		return true
	})
}

// Count returns the current number of active sessions.
func (m *UDPSessionManager) Count() int64 {
	return m.count.Load()
}

// StoreInputSource persists a deep-copied Header as the session's input source
// mapping. This is called ONLY after trust+policy check passes — untrusted
// PROXY metadata must never be persisted as session state.
// SECURITY INVARIANT: caller must verify trust before calling this.
func StoreInputSource(s *UDPSession, hdr proxyproto.Header, src proxyproto.Source) {
	cloned := cloneHeader(hdr)
	s.inputSource.Store(&cloned)
	s.inputSrcKind.Store(&src)
}
