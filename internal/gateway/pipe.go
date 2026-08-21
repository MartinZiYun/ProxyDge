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

	"proxydge/internal/transport"
)

// pipeStream bidirectionally copies between a client and an upstream.
// clientReader is the source for the client→upstream direction (may be a
// *bufio.Reader with peeked bytes from header detection). clientWriter and
// upstream must implement io.ReadWriteCloser; if they also implement
// transport.CloseWriter, half-close is used to signal EOF on that direction.
func pipeStream(clientReader io.Reader, clientWriter, upstream io.ReadWriteCloser,
	log *slog.Logger, remote net.Addr,
) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(upstream, clientReader); err != nil {
			log.Debug("pipe error: client→upstream", "remote", remote, "err", err)
		}
		if cw, ok := upstream.(transport.CloseWriter); ok {
			_ = cw.CloseWrite() // client→upstream done → tell downstream via FIN
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := io.Copy(clientWriter, upstream); err != nil {
			log.Debug("pipe error: upstream→client", "remote", remote, "err", err)
		}
		if cw, ok := clientWriter.(transport.CloseWriter); ok {
			_ = cw.CloseWrite() // upstream→client done → tell client via FIN
		}
	}()
	wg.Wait()
}
