// Package transport defines the gateway's own connection abstraction so the
// gateway does not depend directly on concrete *net.TCPConn / net.Listener
// types (enabling fakes in tests and transport swaps). The TCP adapters here
// are the only production implementation.
package transport

import (
	"fmt"
	"net"
)

// Conn is a duplex byte stream with address access and half-close.
// *net.TCPConn satisfies it directly.
type Conn interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	CloseWrite() error
}

// Listener accepts inbound transport.Conn connections.
type Listener interface {
	Accept() (Conn, error)
	Close() error
	Addr() net.Addr
}

// Dialer dials outbound transport.Conn connections.
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

func (l tcpListener) Close() error  { return l.ln.Close() }
func (l tcpListener) Addr() net.Addr { return l.ln.Addr() }

// Listen creates a TCP Listener.
func Listen(network, address string) (Listener, error) {
	ln, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf("transport: %q is not tcp", network)
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
		return nil, fmt.Errorf("transport: %q is not tcp", network)
	}
	return tc, nil
}
