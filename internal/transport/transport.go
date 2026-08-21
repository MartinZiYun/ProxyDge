// Package transport defines cross-transport connection abstractions shared
// by all connection-oriented transports. Transport-specific semantics
// (half-close, read deadlines, datagram boundaries) live in transport-specific
// packages (e.g. internal/tcp). This package holds only interfaces — no
// concrete implementations.
package transport

import "net"

// AddrConn provides address access without I/O. Separated from Conn so
// proxyproto can build headers from any address-bearing connection without
// importing a concrete transport.
type AddrConn interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// Conn is the minimal I/O connection abstraction shared by
// connection-oriented transports. Transport-specific semantics
// remain in transport-specific packages.
//
// Note: this is not a claim that all transports fit this interface. A
// datagram-oriented transport (e.g. UDP) may define its own connection
// abstraction with ReadFrom/WriteTo rather than satisfying Conn.
type Conn interface {
	AddrConn
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
}

// CloseWriter is an optional capability interface for connection-oriented
// transports that support half-close (e.g. TCP's FIN). Transports without
// half-close simply don't implement it. Callers check via type assertion;
// it is NOT embedded in Conn — a connection may or may not satisfy it.
type CloseWriter interface {
	CloseWrite() error
}

// RemoteIP extracts the remote IP from a connection's peer address. Handles
// TCP now; UDP can be added later. A helper, not a core abstraction.
func RemoteIP(c AddrConn) net.IP {
	switch a := c.RemoteAddr().(type) {
	case *net.TCPAddr:
		return a.IP
	}
	return nil
}
