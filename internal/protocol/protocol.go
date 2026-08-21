// Package protocol defines the Protocol enum used for routing and
// configuration. It is intentionally a lightweight string type with no
// transport behavior — it labels which transport a gateway or listener
// should use, nothing more. Future transports (UDP, QUIC) add constants
// here; the type itself never carries I/O semantics.
package protocol

// Protocol identifies a transport protocol for routing and configuration.
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp" // reserved; no implementation yet
)
