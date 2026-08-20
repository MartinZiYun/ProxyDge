// Command proxydge is a PROXY Protocol normalizer. It listens on a TCP port,
// accepts upstream connections that are either direct or carry a PROXY
// Protocol v1/v2 header, normalizes every connection to a PROXY Protocol v2
// header, and pipes it to a single configurable downstream.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"proxydge/internal/gateway"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses flags, wires the adapters, and serves until a signal arrives or
// the listener fails. It returns the process exit code (2 for usage errors,
// 1 for runtime errors, 0 on clean shutdown). Wired here so tests can cover
// the flag/exit-code paths without touching the network.
func run(args []string) int {
	fs := flag.NewFlagSet("proxydge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", ":9000", "listen address (host:port)")
	upstream := fs.String("upstream", "", "downstream target host:port (required)")
	policyStr := fs.String("policy", "use", "upstream header policy: use|require|reject")
	detectTimeout := fs.Duration("detect-timeout", time.Second, "PROXY header detection timeout")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "proxydge: -upstream is required")
		return 2
	}
	policy, ok := parsePolicy(*policyStr)
	if !ok {
		fmt.Fprintf(os.Stderr, "proxydge: invalid -policy %q (use|require|reject)\n", *policyStr)
		return 2
	}

	ln, err := transport.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: listen: %v\n", err)
		return 1
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	g := gateway.New(
		ln, transport.TCPDialer{},
		goproxyproto.NewReader(), goproxyproto.NewWriter(),
		policy, *upstream, *detectTimeout, logger,
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

func parsePolicy(s string) (gateway.Policy, bool) {
	switch s {
	case "use":
		return gateway.PolicyUse, true
	case "require":
		return gateway.PolicyRequire, true
	case "reject":
		return gateway.PolicyReject, true
	}
	return 0, false
}
