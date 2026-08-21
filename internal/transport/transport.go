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
