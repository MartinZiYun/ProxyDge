// Command proxydge is a PROXY Protocol normalizer. It listens on a TCP port,
// accepts upstream connections that are either direct or carry a PROXY
// Protocol v1/v2 header, normalizes every connection to a PROXY Protocol v2
// header, and pipes it to a single configurable downstream.
//
// Usage: proxydge <command> [options]
//   start    Run the gateway.
//   init     Write a sample config.yaml.
//   version  Print version and build info.
//   help     Show usage.
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
	"runtime/debug"
	"syscall"

	"proxydge/internal/config"
	"proxydge/internal/gateway"
	"proxydge/internal/proxyproto/goproxyproto"
	"proxydge/internal/transport"
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
		fmt.Fprint(out, helpText)
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
		fmt.Fprint(out, helpText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "proxydge: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'proxydge help' for usage.")
		return 2
	}
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
		fmt.Fprintln(os.Stderr, err)
		return 2
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

// cmdInit writes a sample config.yaml. -config chooses the path (default: next
// to the executable). Exit 0 on success, 1 on write error, 2 on bad flags.
func cmdInit(args []string) int {
	fs := flag.NewFlagSet("proxydge init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "where to write the sample config (default: <exe-dir>/config.yaml)")
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
	if err := config.WriteSample(path); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: init: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "proxydge: wrote sample config to %s\n", path)
	return 0
}

// cmdVersion prints build info. Uses runtime/debug.BuildInfo so the git revision
// (when built from a VCS checkout) is embedded automatically — no ldflags.
func cmdVersion(args []string) int {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(out, "proxydge (unknown)")
		return 0
	}
	ver := bi.Main.Version
	if ver == "" {
		ver = "(devel)"
	}
	rev, modified := vcsInfo(bi)
	if rev != "" {
		fmt.Fprintf(out, "proxydge %s (rev %s, modified=%v)\n", ver, rev, modified)
	} else {
		fmt.Fprintf(out, "proxydge %s\n", ver)
	}
	return 0
}

// vcsInfo extracts the VCS revision (short) and dirty flag from build settings.
func vcsInfo(bi *debug.BuildInfo) (rev string, modified bool) {
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	return rev, modified
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

const helpText = `ProxyDge — PROXY Protocol normalizer.

Usage:
  proxydge <command> [options]

Commands:
  start     Run the gateway.
  init      Write a sample config.yaml.
  version   Print version and build info.
  help      Show this help.

Configuration precedence (highest to lowest):
  CLI flags  >  env (PROXYDGE_*)  >  config file  >  defaults

The config file is auto-discovered at <executable-dir>/config.yaml; override
with -config. All options below apply to 'start' unless noted.

start options:
  -listen <addr>            listen address (default ":9000")
  -upstream <host:port>     downstream target (required)
  -policy <p>              use|require|reject (default "use")
  -detect-timeout <dur>    PROXY header detection timeout (default 1s)
  -config <path>           config file path
  -log-console-level <l>   debug|info|warn|error (default "info")
  -log-console-format <f>  text|json (default "text")
  -log-file <path>         file log path (empty = disabled)
  -log-file-level <l>      debug|info|warn|error (default "info")
  -log-file-format <f>     text|json (default "json")

init options:
  -config <path>           where to write the sample (default: <exe-dir>/config.yaml)
`
