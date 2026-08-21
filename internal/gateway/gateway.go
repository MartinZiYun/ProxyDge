// Package gateway implements the PROXY-protocol normalizing proxy. It accepts
// inbound connections (direct, PROXY v1, or PROXY v2), normalizes them all to a
// PROXY v2 header, dials a single downstream, writes the header, and pipes
// bytes both ways with TCP half-close. The gateway depends only on the
// proxyproto and transport abstractions — never on the go-proxyproto library
// or the config/slog-sink wiring directly. It receives a single unified
// *slog.Logger and is unaware of how many sinks exist.
package gateway

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"proxydge/internal/proxyproto"
	"proxydge/internal/transport"
)

// Policy controls which upstream PROXY headers are permitted.
type Policy int

const (
	// PolicyUse accepts both PROXY headers and direct connections (default).
	PolicyUse Policy = iota
	// PolicyRequire rejects direct connections; a PROXY header is mandatory.
	PolicyRequire
	// PolicyReject rejects connections that carry a PROXY header (direct only).
	PolicyReject
)

func (p Policy) String() string {
	switch p {
	case PolicyUse:
		return "use"
	case PolicyRequire:
		return "require"
	case PolicyReject:
		return "reject"
	}
	return "unknown"
}

// Gateway normalizes inbound PROXY headers (v1/v2/direct) to v2 and pipes to a
// single downstream target.
type Gateway struct {
	ln            transport.Listener
	dialer        transport.Dialer
	reader        proxyproto.Reader
	writer        proxyproto.Writer
	policy        Policy
	upstream      string
	detectTimeout time.Duration
	log           *slog.Logger
	trust         *TrustChecker
	untrusted     UntrustedAction
}

// New constructs a Gateway. The listener, dialer, reader, writer, and logger
// are injected so the gateway stays free of concrete adapter and sink types.
// detectTimeout bounds PROXY-header detection: if a candidate prefix (first
// byte 'P' or '\r') arrives but no complete signature follows within this
// duration, the connection is treated as direct. Pass 0 to block indefinitely
// (only safe when all upstreams are guaranteed to send a complete header or
// close). logger may be nil (a discarding logger is used).
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

// Serve runs the accept loop. It returns nil when the listener is closed.
func (g *Gateway) Serve() error {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go g.handle(c)
	}
}

// handle normalizes one inbound connection and pipes it to the downstream.
// Policy is applied after detection: require rejects direct, reject rejects
// headers. A malformed detected header is closed without fallback.
func (g *Gateway) handle(c transport.Conn) {
	defer c.Close()

	br := bufio.NewReader(c)
	if g.detectTimeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(g.detectTimeout))
	}
	hdr, src, err := g.reader.Read(br)
	if g.detectTimeout > 0 {
		_ = c.SetReadDeadline(time.Time{}) // clear: the pipe blocks normally
	}
	if err != nil {
		g.log.Error("malformed upstream header", "remote", c.RemoteAddr(), "err", err)
		return
	}

	// Trust check: only trusted networks may send PROXY headers.
	// remoteIP comes from the TCP socket, never from the PROXY header's SrcIP.
	// Policy evaluates the normalized source AFTER trust handling, not the raw
	// presence of a PROXY header.
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

	switch {
	case g.policy == PolicyReject && src != proxyproto.SourceDirect:
		g.log.Info("rejected: policy forbids PROXY header", "remote", c.RemoteAddr(), "policy", g.policy.String())
		return
	case g.policy == PolicyRequire && src == proxyproto.SourceDirect:
		g.log.Info("rejected: policy requires PROXY header", "remote", c.RemoteAddr(), "policy", g.policy.String())
		return
	case src == proxyproto.SourceDirect:
		hdr = proxyproto.HeaderFromConn(c)
	}

	g.log.Info("accept", "remote", c.RemoteAddr(), "source", src, "policy", g.policy.String(), "upstream", g.upstream)

	up, err := g.dialer.Dial("tcp", g.upstream)
	if err != nil {
		g.log.Warn("downstream dial failed", "upstream", g.upstream, "remote", c.RemoteAddr(), "err", err)
		return
	}
	defer up.Close()

	if err := g.writer.WriteTo(up, hdr); err != nil {
		g.log.Error("write v2 header to downstream", "upstream", g.upstream, "err", err)
		return
	}

	// Bidirectional pipe with TCP half-close. br carries any application bytes
	// that were peeked during header detection.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(up, br); err != nil {
			g.log.Debug("pipe error: client→upstream", "remote", c.RemoteAddr(), "err", err)
		}
		_ = up.CloseWrite() // client→upstream done → tell downstream via FIN
	}()
	go func() {
		defer wg.Done()
		if _, err := io.Copy(c, up); err != nil {
			g.log.Debug("pipe error: upstream→client", "remote", c.RemoteAddr(), "err", err)
		}
		_ = c.CloseWrite() // upstream→client done → tell client via FIN
	}()
	wg.Wait()
}

// remoteIP extracts the IP from the connection's real TCP peer address.
// It never uses the PROXY header's claimed SrcIP — trust decisions must be
// based on the socket's RemoteAddr.
func remoteIP(c transport.Conn) net.IP {
	if tcp, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP
	}
	return nil
}
