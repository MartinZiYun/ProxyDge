// Command proxydge is a PROXY Protocol normalizer. It listens on a TCP port,
// accepts upstream connections that are either direct or carry a PROXY
// Protocol v1/v2 header, normalizes every connection to a PROXY Protocol
// header (v1 or v2, per tcp.header-version), and pipes it to a single
// configurable downstream.
//
// Usage: proxydge <command> [options]
//
//	start    Run the gateway.
//	init     Write a sample config.yaml.
//	version  Print version and build info.
//	help     Show usage.
//
// With no command, proxydge prints help.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"proxydge/internal/config"
	"proxydge/internal/gateway"
	"proxydge/internal/i18n"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/tcp"
	"proxydge/internal/udp"
	"proxydge/internal/version"
)

// out is where help/version output goes. It is a package var so tests can
// capture it without piping os.Stdout. Defaults to os.Stdout in production.
var out io.Writer = os.Stdout

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the command dispatcher. With no args it prints help and exits 0.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(out, helpText(nil))
		return 0
	}
	switch args[0] {
	case "start":
		return cmdStart(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "version":
		return cmdVersion(args[1:])
	case "help":
		fmt.Fprint(out, helpText(args[1:]))
		return 0
	default:
		cat, _ := i18n.Load(i18n.DetectLocale(""))
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.unknown_command", args[0]))
		fmt.Fprintln(os.Stderr, cat.T("error.run_help"))
		return 2
	}
}

// helpText loads the locale catalog and returns the translated help text.
// args are the flags after the command (e.g. ["-lang", "zh-CN"] for
// "proxydge help -lang zh-CN"). If -lang is present it overrides
// PROXYDGE_LANG and the system locale; otherwise DetectLocale falls back
// through PROXYDGE_LANG > LANG/LC_ALL > en.
func helpText(args []string) string {
	lang := ""
	if len(args) > 0 {
		fs := flag.NewFlagSet("proxydge help", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.StringVar(&lang, "lang", "", "display language: en|zh-CN")
		_ = fs.Parse(args)
	}
	cat, _ := i18n.Load(i18n.DetectLocale(lang))
	return cat.T("help.text")
}

// cmdStart loads configuration (CLI > env > file > defaults) and runs the
// gateway until a signal or a listener error. Exit: 0 clean, 2 config/usage,
// 1 runtime.
func cmdStart(args []string) int {
	cfg, err := config.Load(args)
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			return 0
		}
		cat, _ := i18n.Load(i18n.DetectLocale(""))
		var ce *config.ConfigError
		if errors.As(err, &ce) {
			fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T(ce.Key, ce.Args...))
		} else {
			fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		}
		return 2
	}

	// Startup banner: version block + loaded config file + per-field source.
	// Printed to stderr (unconditional, for troubleshooting) before binding so
	// it shows even if listen/serve later fails. Captured by journalctl.
	fmt.Fprintln(os.Stderr, version.String())
	fmt.Fprint(os.Stderr, cfg.Describe())

	// Load locale catalog for translating warnings and notices.
	cat, _ := i18n.Load(i18n.DetectLocale(cfg.Lang))

	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "%s: %s\n", cat.T("label.warning"), cat.T(w.Key, w.Args...))
	}
	if key, args := cfg.MigrationNotice(); key != "" {
		fmt.Fprintf(os.Stderr, "%s: %s\n", cat.T("label.notice"), cat.T(key, args...))
	}

	logger, closeFile, err := buildLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: logger: %v\n", err)
		return 1
	}
	defer closeFile()

	trust, err := gateway.NewTrustChecker(cfg.TrustedNetworks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		return 1
	}

	// Branch on protocol: UDP gateway has its own session/datagram model;
	// TCP gateway uses the stream-based pipeStream.
	var errc chan error
	var closer func()

	if cfg.Protocol == "udp" {
		g, err := udp.New(
			cfg.Listen, cfg.Upstream,
			gatewayPolicy(cfg.Policy), trust, untrustedProxyAction(cfg.UntrustedProxyAction),
			udpHeaderMode(cfg.UDPHeaderMode),
			cfg.IdleTimeout, int64(cfg.MaxSessions), cfg.MaxDatagramSize,
			logger,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: udp gateway: %v\n", err)
			return 1
		}
		defer g.Close()
		errc = make(chan error, 1)
		go func() { errc <- g.Serve() }()
		closer = g.Close
	} else {
		ln, err := tcp.Listen("tcp", cfg.Listen)
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: listen: %v\n", err)
			return 1
		}
		g := gateway.New(
			ln, tcp.TCPDialer{},
			goproxyproto.NewReader(), goproxyproto.NewWriter(tcpHeaderVersion(cfg.TCPHeaderVersion)),
			gatewayPolicy(cfg.Policy), cfg.Upstream, cfg.DetectTimeout, cfg.TCPIdleTimeout, logger,
			trust, untrustedProxyAction(cfg.UntrustedProxyAction), familyMismatch(cfg.TCPFamilyMismatch),
			cfg.TCPMaxConnections,
		)
		errc = make(chan error, 1)
		go func() { errc <- g.Serve() }()
		closer = func() { _ = ln.Close() }
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-sigc:
		logger.Info("proxydge: shutting down", "signal", sig.String())
		closer()
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

// cmdInit writes a sample config.yaml. -config chooses the path (default: next
// to the executable). Refuses to overwrite an existing file unless -force is
// given. Exit 0 on success, 1 on write error, 2 on bad flags or file exists.
func cmdInit(args []string) int {
	fs := flag.NewFlagSet("proxydge init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "where to write the sample config (default: <exe-dir>/config.yaml)")
	force := fs.Bool("force", false, "overwrite an existing config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	path := *cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	if !*force {
		if _, err := os.Stat(path); err == nil {
			cat, _ := i18n.Load(i18n.DetectLocale(""))
			fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.init_exists", path))
			return 2
		}
	}
	if err := config.WriteSample(path); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: init: %v\n", err)
		return 1
	}
	cat, _ := i18n.Load(i18n.DetectLocale(""))
	fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.init_wrote", path))
	return 0
}

// cmdVersion prints build info. The metadata comes from the version package
// (ldflags-injected in CI, falling back to debug.BuildInfo for local builds).
//
//	proxydge version           -> detailed multi-line banner
//	proxydge version --short   -> just the SemVer core, e.g. v0.1.0
func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("proxydge version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	short := fs.Bool("short", false, "print only the version (e.g. v0.1.0)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *short {
		fmt.Fprintln(out, version.Short())
	} else {
		fmt.Fprintln(out, version.String())
	}
	return 0
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

// untrustedProxyAction maps the validated config string to the gateway's
// enum. It is in main (not the config package) so the gateway stays free
// of config imports.
func untrustedProxyAction(s string) gateway.UntrustedAction {
	if s == "strip" {
		return gateway.UntrustedStrip
	}
	return gateway.UntrustedReject
}

// tcpHeaderVersion maps the validated config string to the wire version byte
// for the downstream writer. It is in main (not the config package) so the
// gateway and adapter stay free of config imports.
func tcpHeaderVersion(s string) byte {
	if s == "v1" {
		return 1
	}
	return 2
}

// familyMismatch maps the validated config string to the gateway's
// mixed-address-family disposition enum. Same placement rationale as
// untrustedProxyAction.
func familyMismatch(s string) gateway.FamilyMismatchAction {
	switch s {
	case "unknown":
		return gateway.FamilyMismatchUnknown
	case "legacy":
		return gateway.FamilyMismatchLegacy
	default:
		return gateway.FamilyMismatchReject
	}
}

// udpHeaderMode maps the validated config string to the UDP gateway's enum.
// It is in main (not the config package) so the udp package stays free of
// config imports.
func udpHeaderMode(s string) udp.OutputMode {
	if s == "first_datagram" {
		return udp.OutputFirstDatagram
	}
	return udp.OutputEveryDatagram
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
