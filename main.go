// Command proxydge is a PROXY Protocol normalizer. It listens on a TCP port,
// accepts upstream connections that are either direct or carry a PROXY
// Protocol v1/v2 header, normalizes every connection to a PROXY Protocol v2
// header, and pipes it to a single configurable downstream.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"proxydge/internal/config"
	"proxydge/internal/gateway"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run loads configuration (CLI > env > file > defaults, see internal/config),
// wires the adapters, and serves until a signal arrives or the listener fails.
// Exit codes: 0 clean shutdown, 2 usage/config error, 1 runtime error.
func run(args []string) int {
	cfg, err := config.Load(args)
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ln, err := transport.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: listen: %v\n", err)
		return 1
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	g := gateway.New(
		ln, transport.TCPDialer{},
		goproxyproto.NewReader(), goproxyproto.NewWriter(),
		gatewayPolicy(cfg.Policy), cfg.Upstream, cfg.DetectTimeout, logger,
	)

	errc := make(chan error, 1)
	go func() { errc <- g.Serve() }()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-sigc:
		logger.Printf("proxydge: received %s, shutting down", sig)
		_ = ln.Close()
		if err := <-errc; err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: serve: %v\n", err)
			return 1
		}
	case err := <-errc:
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: serve: %v\n", err)
			return 1
		}
	}
	return 0
}

// gatewayPolicy maps the validated policy string to the gateway's enum. It is
// in main (not the config package) so the gateway stays free of config imports.
func gatewayPolicy(s string) gateway.Policy {
	switch s {
	case "require":
		return gateway.PolicyRequire
	case "reject":
		return gateway.PolicyReject
	default:
		return gateway.PolicyUse
	}
}
