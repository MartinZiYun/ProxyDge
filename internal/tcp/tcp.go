// Package tcp defines TCP-specific connection, listener, and dialer types.
// Conn embeds transport.Conn and adds TCP-only methods (half-close and read
// deadline). *net.TCPConn satisfies it directly, so the adapters here are
// thin wrappers that preserve TCP semantics without re-implementing I/O.
package tcp

import (
	"fmt"
	"net"
	"time"

	"proxydge/internal/transport"
)

// Conn is a TCP byte-stream connection with half-close and read-deadline
// control. *net.TCPConn satisfies it directly.
type Conn interface {
	transport.Conn
	CloseWrite() error
	SetReadDeadline(time.Time) error
}

// Listener accepts inbound tcp.Conn connections.
type Listener interface {
	Accept() (Conn, error)
	Close() error
	Addr() net.Addr
}

// Dialer dials outbound tcp.Conn connections.
type Dialer interface {
	Dial(network, address string) (Conn, error)
}

// tcpListener adapts *net.TCPListener to Listener. We hold it as a field
// (not embed) because *net.TCPListener.Addr returns *net.TCPAddr, which does
// not satisfy the interface's net.Addr return type.
type tcpListener struct {
	ln *net.TCPListener
}

func (l tcpListener) Accept() (Conn, error) {
	c, err := l.ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (l tcpListener) Close() error    { return l.ln.Close() }
func (l tcpListener) Addr() net.Addr  { return l.ln.Addr() }

// Listen creates a TCP Listener.
func Listen(network, address string) (Listener, error) {
	ln, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf("tcp: %q is not tcp", network)
	}
	return tcpListener{ln: tl}, nil
}

// TCPDialer is the production TCP dialer. It satisfies Dialer.
type TCPDialer struct{}

func (TCPDialer) Dial(network, address string) (Conn, error) {
	c, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("tcp: %q is not tcp", network)
	}
	return tc, nil
}
