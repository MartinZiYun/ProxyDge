package udp

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"proxydge/internal/gateway"
	"proxydge/internal/proxyproto"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/transport"
)

// OutputMode controls how PROXY headers are emitted to the upstream.
type OutputMode int

const (
	OutputEveryDatagram OutputMode = iota // default: [PROXY][payload] per datagram
	OutputFirstDatagram                   // compat: [PROXY][payload] then [payload]...
)

func (m OutputMode) String() string {
	switch m {
	case OutputEveryDatagram:
		return "every_datagram"
	case OutputFirstDatagram:
		return "first_datagram"
	}
	return "unknown"
}

// udpAddrConn wraps two net.Addr values to satisfy transport.AddrConn,
// allowing gateway.Decide to call HeaderFromAddrs without a real Conn.
type udpAddrConn struct {
	local, remote net.Addr
}

func (a udpAddrConn) LocalAddr() net.Addr  { return a.local }
func (a udpAddrConn) RemoteAddr() net.Addr { return a.remote }

// UDPGateway is a PROXY Protocol normalizer for UDP datagrams. It accepts
// datagrams (direct, PROXY v1/v2), normalizes them, and forwards to a single
// downstream via per-session connected UDP sockets.
type UDPGateway struct {
	listener        *net.UDPConn
	localAddr       net.Addr     // cached listener.LocalAddr()
	upstreamAddr    *net.UDPAddr // pre-resolved in New()
	dgReader        proxyproto.DatagramReader
	dgWriter        proxyproto.DatagramWriter
	policy          gateway.Policy
	trust           *gateway.TrustChecker
	untrusted       gateway.UntrustedAction
	outputMode      OutputMode
	idleTimeout     time.Duration
	maxSessions     int64
	maxDatagramSize int
	log             *slog.Logger
	manager         *UDPSessionManager
}

// New constructs a UDPGateway. The upstream address is resolved once here;
// each session calls DialUDP with the pre-resolved *net.UDPAddr.
func New(
	listenAddr, upstream string,
	policy gateway.Policy,
	trust *gateway.TrustChecker,
	untrusted gateway.UntrustedAction,
	outputMode OutputMode,
	idleTimeout time.Duration,
	maxSessions int64,
	maxDatagramSize int,
	logger *slog.Logger,
) (*UDPGateway, error) {
	// Resolve upstream once — no per-session DNS.
	upstreamUDP, err := net.ResolveUDPAddr("udp", upstream)
	if err != nil {
		return nil, err
	}
	// Bind listener.
	lc := net.ListenConfig{}
	pc, err := lc.ListenPacket(nil, "udp", listenAddr)
	if err != nil {
		return nil, err
	}
	listener := pc.(*net.UDPConn)

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &UDPGateway{
		listener:        listener,
		localAddr:       listener.LocalAddr(),
		upstreamAddr:    upstreamUDP,
		dgReader:        goproxyproto.NewDatagramReader(),
		dgWriter:        goproxyproto.NewDatagramWriter(),
		policy:          policy,
		trust:           trust,
		untrusted:       untrusted,
		outputMode:      outputMode,
		idleTimeout:     idleTimeout,
		maxSessions:     maxSessions,
		maxDatagramSize: maxDatagramSize,
		log:             logger,
		manager:         NewUDPSessionManager(maxSessions),
	}, nil
}

// Serve runs the accept loop. Reads datagrams from the listener and dispatches
// to handleDatagram. Returns when the listener is closed.
// maxDatagramSize is the maximum size of the complete received UDP datagram
// (including any PROXY Protocol header). Datagrams exceeding this are dropped
// — never truncated, never parsed.
//
// The receive buffer is sized larger than maxDatagramSize so that an oversized
// datagram can be fully read and explicitly dropped. A buffer sized exactly
// maxDatagramSize would silently truncate, making the size check unreliable.
func (g *UDPGateway) Serve() error {
	// +1 is sufficient: ReadFromUDP returns at most len(buf); if n > maxDatagramSize,
	// we know the datagram exceeded the limit. We don't need the full oversized
	// content — just enough to detect the overflow.
	buf := make([]byte, g.maxDatagramSize+1)
	for {
		n, peer, err := g.listener.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Drop oversized datagrams — never truncate, never parse a truncated header.
		if n > g.maxDatagramSize {
			g.log.Debug("oversized datagram dropped", "remote", peer, "size", n, "max", g.maxDatagramSize)
			continue
		}
		// Copy the datagram — the buffer is reused for the next ReadFromUDP.
		data := make([]byte, n)
		copy(data, buf[:n])
		g.handleDatagram(data, peer)
	}
}

// Close stops the gateway: closes listener and expires all sessions.
func (g *UDPGateway) Close() {
	_ = g.listener.Close()
	g.manager.ExpireAll()
}

// handleDatagram implements the security-ordered pipeline:
//
//  1. ParseDatagram → normalized representation
//  2. Session lookup (read-only — no creation yet)
//  3. Recover input flow state (first_datagram input, existing session)
//  4. Decide() ← trust (ActualPeer) + policy
//  5. If rejected → DROP (no session, no DialUDP, no goroutine)
//  6. If allowed + new → create session / DialUDP / start reader
//  7. Persist inputSource (AFTER trust pass, deep-copied)
//  8. Encode output (FormatDatagram or raw, per outputMode)
//  9. Forward to upstream
//
// 10. Refresh idle timer
func (g *UDPGateway) handleDatagram(data []byte, actualPeer *net.UDPAddr) {
	// 1. Parse datagram
	hdr, payload, src, err := g.dgReader.ParseDatagram(data)
	if err != nil {
		g.log.Debug("malformed PROXY header", "remote", actualPeer, "err", err)
		return
	}

	// 2. Session lookup (read-only)
	key := keyFromUDPAddr(actualPeer)
	var sess *UDPSession
	if v, ok := g.manager.Load(key); ok {
		sess = v
		// Check if expired
		select {
		case <-sess.done:
			sess = nil // expired — treat as new
		default:
		}
	}

	// 3. Recover input flow state (first_datagram input mode)
	if sess != nil && src == proxyproto.SourceDirect {
		if storedHdr := sess.inputSource.Load(); storedHdr != nil {
			if storedSrc := sess.inputSrcKind.Load(); storedSrc != nil {
				hdr = *storedHdr
				src = *storedSrc
			}
		}
	}

	// 4. Decide() — trust + policy (BEFORE session creation)
	ac := udpAddrConn{local: g.localAddr, remote: actualPeer}
	origSrc := src
	hdr, src, allow, reason := gateway.Decide(
		g.policy, g.trust, g.untrusted,
		src, hdr, transport.RemoteIP(ac), ac,
	)

	// Log based on reason (preserving TCP gateway log semantics)
	if reason == "strip" {
		g.log.Info("stripped: untrusted source PROXY header", "remote", actualPeer, "source", origSrc)
	}
	if !allow {
		switch reason {
		case "untrusted":
			g.log.Info("rejected: untrusted source with PROXY header", "remote", actualPeer, "source", origSrc)
		case "policy:forbids":
			g.log.Info("rejected: policy forbids PROXY header", "remote", actualPeer, "policy", g.policy.String())
		case "policy:requires":
			g.log.Info("rejected: policy requires PROXY header", "remote", actualPeer, "policy", g.policy.String())
		}
		return // 5. DROP — no session created, zero resources consumed
	}

	// 6. Create session if new
	if sess == nil {
		upstream, err := net.DialUDP("udp", nil, g.upstreamAddr)
		if err != nil {
			g.log.Warn("upstream dial failed", "remote", actualPeer, "err", err)
			return
		}
		sess, err = g.manager.Create(key, actualPeer, g.listener, upstream, g.idleTimeout, g.log)
		if err != nil {
			_ = upstream.Close()
			if errors.Is(err, ErrMaxSessions) {
				g.log.Debug("max sessions reached, dropping", "remote", actualPeer)
			} else {
				g.log.Warn("session create failed", "remote", actualPeer, "err", err)
			}
			return
		}
		// Log session creation at Info (once per session, like TCP's "accept").
		g.log.Info("accept", "remote", actualPeer, "source", src, "policy", g.policy.String(), "output", g.outputMode.String())
	} else {
		// Per-datagram accept at Debug (avoids log flooding at high dps).
		g.log.Debug("datagram", "remote", actualPeer, "source", src)
	}

	// 7. Persist inputSource (AFTER trust check, deep-copied)
	//    Only for first_datagram input mode: store the PROXY header source
	//    so subsequent headerless datagrams can recover it.
	if src != proxyproto.SourceDirect && sess.inputSource.Load() == nil {
		StoreInputSource(sess, hdr, src)
	}

	// 8. Encode output
	var encoded []byte
	if g.outputMode == OutputEveryDatagram {
		encoded, err = g.dgWriter.FormatDatagram(hdr, payload)
		if err != nil {
			g.log.Error("format datagram", "remote", actualPeer, "err", err)
			return
		}
	} else { // OutputFirstDatagram
		if !sess.headerSent.Swap(true) {
			// First datagram — include PROXY header
			encoded, err = g.dgWriter.FormatDatagram(hdr, payload)
			if err != nil {
				g.log.Error("format datagram", "remote", actualPeer, "err", err)
				return
			}
		} else {
			// Subsequent datagrams — raw payload
			encoded = payload
		}
	}

	// 9. Forward to upstream
	_, err = sess.upstream.Write(encoded)
	if err != nil {
		// Write errors do NOT trigger session expiry (transient UDP errors)
		g.log.Debug("upstream write failed", "remote", actualPeer, "err", err)
	}

	// 10. Refresh idle timer
	sess.refresh()
}
