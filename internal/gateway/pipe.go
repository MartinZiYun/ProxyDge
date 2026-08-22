// pipe.go extracts the bidirectional pipe logic from handle() so it can be
// reused across transports. The function checks for transport.CloseWriter via
// type assertion — TCP connections satisfy it (FIN half-close); a future UDP
// transport wouldn't, and the half-close is simply skipped.
package gateway

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"proxydge/internal/transport"
)

// deadlineSetter is an optional capability for connections that support
// SetReadDeadline (e.g. *net.TCPConn). Used by pipeStream to enforce a
// per-direction idle timeout. Transports without it simply skip the timeout.
type deadlineSetter interface {
	SetReadDeadline(time.Time) error
}

// deadlineReader wraps an io.Reader and resets the read deadline on conn
// before each Read call. If no data arrives within timeout, the Read returns
// a deadline-exceeded error, causing io.Copy to exit and the pipe to
// half-close. Each direction has its own deadlineReader — activity in one
// direction does NOT extend the other direction's deadline.
type deadlineReader struct {
	r       io.Reader
	conn    deadlineSetter
	timeout time.Duration
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	_ = d.conn.SetReadDeadline(time.Now().Add(d.timeout))
	return d.r.Read(p)
}

// pipeStream bidirectionally copies between a client and an upstream.
// clientReader is the source for the client→upstream direction (may be a
// *bufio.Reader with peeked bytes from header detection). clientWriter and
// upstream must implement io.ReadWriteCloser; if they also implement
// transport.CloseWriter, half-close is used to signal EOF on that direction.
// If idleTimeout > 0 and the connections support SetReadDeadline, each
// direction gets an independent idle timer: if no data flows for idleTimeout
// in a given direction, that side's Read times out and the pipe half-closes.
func pipeStream(clientReader io.Reader, clientWriter, upstream io.ReadWriteCloser,
	log *slog.Logger, remote net.Addr, idleTimeout time.Duration,
) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r := clientReader
		if idleTimeout > 0 {
			if ds, ok := clientWriter.(deadlineSetter); ok {
				r = &deadlineReader{r: r, conn: ds, timeout: idleTimeout}
			}
		}
		if _, err := io.Copy(upstream, r); err != nil {
			log.Debug("pipe error: client→upstream", "remote", remote, "err", err)
		}
		if cw, ok := upstream.(transport.CloseWriter); ok {
			_ = cw.CloseWrite() // client→upstream done → tell downstream via FIN
		}
	}()
	go func() {
		defer wg.Done()
		var r io.Reader = upstream
		if idleTimeout > 0 {
			if ds, ok := upstream.(deadlineSetter); ok {
				r = &deadlineReader{r: r, conn: ds, timeout: idleTimeout}
			}
		}
		if _, err := io.Copy(clientWriter, r); err != nil {
			log.Debug("pipe error: upstream→client", "remote", remote, "err", err)
		}
		if cw, ok := clientWriter.(transport.CloseWriter); ok {
			_ = cw.CloseWrite() // upstream→client done → tell client via FIN
		}
	}()
	wg.Wait()
}
