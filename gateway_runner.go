package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"proxydge/internal/config"
	"proxydge/internal/gateway"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/tcp"
	"proxydge/internal/udp"
)

// runGateway creates and starts a TCP or UDP gateway based on the loaded
// configuration. It returns:
//   - closer: a function that shuts down the gateway (closing the listener or
//     the UDP gateway itself)
//   - errc: a read-only channel that receives the gateway's Serve() result
//     exactly once. The channel is buffered (capacity 1) so the Serve goroutine
//     never blocks, regardless of whether closer is called first.
//   - err: non-nil if the gateway could not be created (bad config, listen
//     failure, etc.)
//
// runGateway does NOT handle signals or fatal-error exit — callers decide how
// to respond (cmdStart uses signals; proxydgeService uses os.Exit).
func runGateway(cfg *config.Config) (closer func(), errc <-chan error, err error) {
	logger, closeFile, err := buildLogger(cfg)
	if err != nil {
		closeFile()
		return nil, nil, fmt.Errorf("logger: %w", err)
	}

	trust, err := gateway.NewTrustChecker(cfg.TrustedNetworks)
	if err != nil {
		closeFile()
		return nil, nil, err
	}

	// Internal error channel — buffered so the Serve goroutine never blocks.
	ch := make(chan error, 1)

	if cfg.Protocol == "udp" {
		g, err := udp.New(
			cfg.Listen, cfg.Upstream,
			gatewayPolicy(cfg.Policy), trust, untrustedProxyAction(cfg.UntrustedProxyAction),
			udpHeaderMode(cfg.UDPHeaderMode),
			cfg.IdleTimeout, int64(cfg.MaxSessions), cfg.MaxDatagramSize,
			logger,
		)
		if err != nil {
			closeFile()
			return nil, nil, fmt.Errorf("udp gateway: %w", err)
		}
		go func() { ch <- g.Serve() }()
		closer = func() {
			g.Close()
			closeFile()
		}
	} else {
		ln, err := tcp.Listen("tcp", cfg.Listen)
		if err != nil {
			closeFile()
			return nil, nil, fmt.Errorf("listen: %w", err)
		}
		g := gateway.New(
			ln, tcp.TCPDialer{},
			goproxyproto.NewReader(), goproxyproto.NewWriter(tcpHeaderVersion(cfg.TCPHeaderVersion)),
			gatewayPolicy(cfg.Policy), cfg.Upstream, cfg.DetectTimeout, cfg.TCPIdleTimeout, logger,
			trust, untrustedProxyAction(cfg.UntrustedProxyAction), familyMismatch(cfg.TCPFamilyMismatch),
			cfg.TCPMaxConnections,
		)
		go func() { ch <- g.Serve() }()
		closer = func() {
			_ = ln.Close()
			closeFile()
		}
	}

	return closer, ch, nil
}

// buildLogger constructs the unified *slog.Logger from the two independent
// sinks (console=stderr, file=cfg.LogFilePath). Each sink gets its own level
// and format (text/json). The gateway receives this single logger and is
// unaware of how many sinks exist.
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

	// Resolve relative log file path to absolute — service processes may
	// have a different working directory than the interactive terminal.
	logPath := cfg.LogFilePath
	if !filepath.IsAbs(logPath) {
		if abs, err := filepath.Abs(logPath); err == nil {
			logPath = abs
		}
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, noop, fmt.Errorf("open log file %s: %w", logPath, err)
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
