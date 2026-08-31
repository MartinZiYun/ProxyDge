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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kardianos/service"

	"proxydge/internal/config"
	"proxydge/internal/gateway"
	"proxydge/internal/i18n"
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
	case "service":
		return cmdService(args[1:])
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

	// When running under the OS service manager (Windows SCM, systemd, etc.),
	// delegate to proxydgeService which implements service.Interface.
	// On interactive terminals, use the normal signal-handling path.
	if !service.Interactive() {
		svcCfg := &service.Config{
			Name:        "ProxyDge",
			DisplayName: "ProxyDge Gateway",
			Description: "PROXY Protocol normalizing gateway for TCP and UDP",
		}
		svc, err := service.New(&proxydgeService{cfg: cfg}, svcCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: service: %v\n", err)
			return 1
		}
		if err := svc.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: service run: %v\n", err)
			return 1
		}
		return 0
	}

	closer, errc, err := runGateway(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		return 1
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigc:
		closer()
	case err := <-errc:
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: serve: %v\n", err)
			return 1
		}
		return 0
	}
	if err := <-errc; err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: serve: %v\n", err)
		return 1
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
