// Package gateway implements the PROXY-protocol normalizing proxy. It accepts
// inbound connections (direct, PROXY v1, or PROXY v2), normalizes them all to
// a PROXY Protocol header in the configured output version (tcp.header-version,
// v1 or v2), applies the configured mixed-address-family disposition
// (tcp.family-mismatch), dials a single downstream, writes the header, and
// pipes bytes both ways with TCP half-close. The gateway depends only on the
// proxyproto and tcp abstractions — never on the go-proxyproto library
// or the config/slog-sink wiring directly. It receives a single unified
// *slog.Logger and is unaware of how many sinks exist.
package gateway

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"time"

	"proxydge/internal/proxyproto"
	"proxydge/internal/tcp"
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

// FamilyMismatchAction selects how the gateway disposes of connections whose
// FINAL downstream header fails proxyproto.FamilyMatchesAddrs — i.e. the
// header's declared family contradicts what its addresses actually express
// (the only wire-craftable shape being AF_INET6 carrying an ::ffff:-mapped
// IPv4 address). The check runs after Decide(), so stripped/direct headers
// (rebuilt from socket addresses, always self-consistent) never trip it.
type FamilyMismatchAction int

const (
	// FamilyMismatchReject closes the connection before dialing downstream:
	// a clean, explicit failure that hands downstream zero interpretable data.
	FamilyMismatchReject FamilyMismatchAction = iota
	// FamilyMismatchUnknown rewrites the header to the address-less form —
	// the writer then emits "PROXY UNKNOWN\r\n" (v1) or a LOCAL+AF_UNSPEC
	// frame (v2) — telling downstream per spec to fall back to its own view.
	FamilyMismatchUnknown
	// FamilyMismatchLegacy skips detection entirely: headers flow into the
	// version-parameterized writer unguarded, whose library-native coercion
	// reproduces the historical behavior byte-for-byte (including silent
	// ::ffff:-mapped emission). Escape hatch for pre-v3 deployments; the
	// startup banner warns when it is selected.
	FamilyMismatchLegacy
)

func (a FamilyMismatchAction) String() string {
	switch a {
	case FamilyMismatchReject:
		return "reject"
	case FamilyMismatchUnknown:
		return "unknown"
	case FamilyMismatchLegacy:
		return "legacy"
	}
	return "unknown-action"
}

// Gateway normalizes inbound PROXY headers (v1/v2/direct) to v2 and pipes to a
// single downstream target.
type Gateway struct {
	ln              tcp.Listener
	dialer          tcp.Dialer
	reader          proxyproto.Reader
	writer          proxyproto.Writer
	policy          Policy
	upstream        string
	detectTimeout   time.Duration
	pipeIdleTimeout time.Duration
	log             *slog.Logger
	trust           *TrustChecker
	untrusted       UntrustedAction
	mixedFamily     FamilyMismatchAction
}

// New constructs a Gateway. The listener, dialer, reader, writer, and logger
// are injected so the gateway stays free of concrete adapter and sink types.
// detectTimeout bounds PROXY-header detection: if a candidate prefix (first
// byte 'P' or '\r') arrives but no complete signature follows within this
// duration, the connection is treated as direct. Pass 0 to block indefinitely
// (only safe when all upstreams are guaranteed to send a complete header or
// close). pipeIdleTimeout bounds the bidirectional pipe after header detection:
// if no data flows in a direction for this duration, that side is closed.
// Pass 0 to disable (pipe blocks indefinitely — DoS risk in untrusted envs).
// mixedFamily selects the disposition for headers whose declared family
// contradicts their addresses (see FamilyMismatchAction). logger may be nil
// (a discarding logger is used).
func New(ln tcp.Listener, dialer tcp.Dialer, r proxyproto.Reader, w proxyproto.Writer, policy Policy, upstream string, detectTimeout, pipeIdleTimeout time.Duration, logger *slog.Logger, trust *TrustChecker, untrusted UntrustedAction, mixedFamily FamilyMismatchAction) *Gateway {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Gateway{
		ln:              ln,
		dialer:          dialer,
		reader:          r,
		writer:          w,
		policy:          policy,
		upstream:        upstream,
		detectTimeout:   detectTimeout,
		pipeIdleTimeout: pipeIdleTimeout,
		log:             logger,
		trust:           trust,
		untrusted:       untrusted,
		mixedFamily:     mixedFamily,
	}
}

// Serve runs the accept loop. It returns nil when the listener is closed.
// Transient accept errors (fd exhaustion, EINTR) are retried after a short
// backoff instead of terminating the gateway.
func (g *Gateway) Serve() error {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if IsTemporaryNetError(err) {
				g.log.Warn("transient accept error", "err", err)
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		go g.handle(c)
	}
}

// IsTemporaryNetError reports whether err is a transient condition that
// accept/read loops should tolerate with a retry instead of terminating:
// EINTR/EAGAIN/EMFILE/ENFILE (POSIX errno values) or a timeout.
func IsTemporaryNetError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EINTR, syscall.EAGAIN, syscall.EMFILE, syscall.ENFILE:
			return true
		}
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// handle normalizes one inbound connection and pipes it to the downstream.
// TCP-specific steps (header detection, dial) are inline; transport-agnostic
// logic (trust+policy decision, bidirectional pipe) is delegated to decide()
// and pipeStream().
func (g *Gateway) handle(c tcp.Conn) {
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

	// Transport-agnostic: trust + policy decision. origSrc is saved before
	// decide() so the caller can log the original source for the strip case
	// (decide modifies src to SourceDirect when stripping).
	origSrc := src
	hdr, src, allow, reason := Decide(g.policy, g.trust, g.untrusted, src, hdr, transport.RemoteIP(c), c)
	if reason == "strip" {
		g.log.Info("stripped: untrusted source PROXY header", "remote", c.RemoteAddr(), "source", origSrc)
	}
	if !allow {
		switch reason {
		case "untrusted":
			g.log.Info("rejected: untrusted source with PROXY header", "remote", c.RemoteAddr(), "source", origSrc)
		case "policy:forbids":
			g.log.Info("rejected: policy forbids PROXY header", "remote", c.RemoteAddr(), "policy", g.policy.String())
		case "policy:requires":
			g.log.Info("rejected: policy requires PROXY header", "remote", c.RemoteAddr(), "policy", g.policy.String())
		}
		return
	}

	// Address-family guard on the FINAL downstream header — i.e. what this
	// connection would actually write after Decide() (a stripped header was
	// rebuilt from socket addresses and is self-consistent by construction,
	// so legitimate direct/strip flows never trip here). legacy skips the
	// check entirely: headers reach the writer unguarded, whose library-
	// native coercion reproduces historical bytes including ::ffff: mapping.
	if g.mixedFamily != FamilyMismatchLegacy && !proxyproto.FamilyMatchesAddrs(hdr) {
		if g.mixedFamily == FamilyMismatchReject {
			g.log.Info("rejected: header family contradicts its addresses", "remote", c.RemoteAddr())
			return
		}
		// unknown: rewrite to the address-less form. The writer turns the
		// zero header into PROXY UNKNOWN / LOCAL+AF_UNSPEC per negotiated
		// version, so downstream is told to fall back rather than being fed
		// addresses that would need coercion to represent.
		g.log.Info("family mismatch: forwarding address-less header", "remote", c.RemoteAddr())
		hdr = proxyproto.Header{}
	}

	g.log.Info("accept", "remote", c.RemoteAddr(), "source", src, "policy", g.policy.String(), "upstream", g.upstream)

	// TCP-specific: dial downstream.
	up, err := g.dialer.Dial("tcp", g.upstream)
	if err != nil {
		g.log.Warn("downstream dial failed", "upstream", g.upstream, "remote", c.RemoteAddr(), "err", err)
		return
	}
	defer up.Close()

	if err := g.writer.WriteTo(up, hdr); err != nil {
		g.log.Error("write header to downstream", "upstream", g.upstream, "err", err)
		return
	}

	// Transport-agnostic: bidirectional pipe with optional half-close.
	// br carries any application bytes that were peeked during header detection.
	pipeStream(br, c, up, g.log, c.RemoteAddr(), g.pipeIdleTimeout)
}
