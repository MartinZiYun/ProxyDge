package tcp

import (
	"io"
	"testing"
)

// TestTCPRoundTripAndCloseWrite verifies the TCP adapters carry bytes and that
// CloseWrite on one end surfaces as an EOF read on the peer (a FIN is delivered).
func TestTCPRoundTripAndCloseWrite(t *testing.T) {
	ln, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverDone := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		// Echo everything back; when the peer CloseWrites, our Read sees EOF,
		// io.Copy returns, and we close the server side.
		_, _ = io.Copy(c, c)
		_ = c.Close()
		serverDone <- nil
	}()

	up, err := TCPDialer{}.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer up.Close()

	msg := []byte("hello")
	if _, err := up.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(up, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo: want %q, got %q", msg, buf)
	}

	// Half-close the write side; the server must observe EOF (CloseWrite → FIN).
	if err := up.CloseWrite(); err != nil {
		t.Fatalf("closewrite: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server: %v", err)
	}
}
