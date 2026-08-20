// Command proxydge is a PROXY Protocol normalizer. It listens on a TCP port,
// accepts upstream connections that are either direct or carry a PROXY
// Protocol v1/v2 header, normalizes every connection to a PROXY Protocol v2
// header, and pipes it to a single configurable downstream.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// Exit codes: 0 clean shutdown / -init written, 2 usage/config error, 1 runtime.
func run(args []string) int {
	cfg, err := config.Load(args)
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// -init: write a sample config and exit.
	if cfg.Init {
		if err := config.WriteSample(cfg.InitPath); err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: -init: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "proxydge: wrote sample config to %s\n", cfg.InitPath)
		return 0
	}

	ln, err := transport.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: listen: %v\n", err)
		return 1
	}

	logger, closeFile, err := buildLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: logger: %v\n", err)
		return 1
	}
	defer closeFile()

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
		logger.Info("proxydge: shutting down", "signal", sig.String())
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

// buildLogger constructs the unified *slog.Logger from the two independent
// sinks (console=stderr, file=cfg.LogFilePath). Each sink gets its own level
// and format (text/json). The gateway receives this single logger and is
// unaware of how many sinks exist. The returned closeFile closes the file
// sink (if any).
func buildLogger(cfg *config.Config) (*slog.Logger, func(), error) {
	noop := func() {}
	var console slog.Handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogConsoleLevel),
	})
	if cfg.LogConsoleFormat == "json" {
		console = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: parseLevel(cfg.LogConsoleLevel),
		})
	}
	if cfg.LogFilePath == "" {
		return slog.New(console), noop, nil
	}

	f, err := os.OpenFile(cfg.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, noop, fmt.Errorf("open log file %s: %w", cfg.LogFilePath, err)
	}
	var file slog.Handler = slog.NewTextHandler(f, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogFileLevel),
	})
	if cfg.LogFileFormat == "json" {
		file = slog.NewJSONHandler(f, &slog.HandlerOptions{
			Level: parseLevel(cfg.LogFileLevel),
		})
	}
	mh := &multiHandler{console: console, file: file}
	return slog.New(mh), func() { _ = f.Close() }, nil
}

// parseLevel maps a config level string to slog.Level.
func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
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

// multiHandler fans a record out to console and file handlers, each with its
// own level threshold and format. Per-sink level filtering happens in each
// sub-handler's Enabled (checked in Handle so a record is only written to the
// sinks that are enabled at that level).
type multiHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.console.Enabled(ctx, level) || h.file.Enabled(ctx, level)
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.console.Enabled(ctx, r.Level) {
		if err := h.console.Handle(ctx, r); err != nil {
			return err
		}
	}
	if h.file.Enabled(ctx, r.Level) {
		if err := h.file.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &multiHandler{console: h.console.WithAttrs(attrs), file: h.file.WithAttrs(attrs)}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	return &multiHandler{console: h.console.WithGroup(name), file: h.file.WithGroup(name)}
}
