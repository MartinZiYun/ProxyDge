package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kardianos/service"

	"proxydge/internal/config"
	"proxydge/internal/i18n"
)

// newServiceControllerFunc is the factory used by service commands to create
// a service.Service. Tests replace this with a fake.
var newServiceControllerFunc = newServiceController

// newServiceController creates the kardianos service.Service with the given
// config path recorded in the service arguments.
// configPath must be an absolute path.
func newServiceController(configPath string) (service.Service, error) {
	svcConfig := &service.Config{
		Name:        "ProxyDge",
		DisplayName: "ProxyDge Gateway",
		Description: "PROXY Protocol normalizing gateway for TCP and UDP",
		Arguments:   []string{"start", "-config", configPath},
		Option: service.KeyValue{
			"DelayedAutoStart":  true,
			"OnFailure":         "restart",
			"Restart":           "always",
			"SuccessExitStatus": "0",
		},
	}
	return service.New(&serviceNoOp{}, svcConfig)
}

// cmdService is the dispatcher for "proxydge service <action>".
func cmdService(args []string) int {
	if len(args) == 0 {
		cat, _ := i18n.Load(i18n.DetectLocale(""))
		fmt.Fprintf(os.Stderr, "proxydge service: %s\n", cat.T("error.run_help"))
		return 2
	}

	action := args[0]
	actionArgs := args[1:]

	// Parse shared flags (-lang) from remaining args.
	fs := flag.NewFlagSet("proxydge service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lang := fs.String("lang", "", "display language")
	_ = fs.Parse(actionArgs)
	cat, _ := i18n.Load(i18n.DetectLocale(*lang))

	switch action {
	case "install":
		return serviceInstall(actionArgs, cat)
	case "uninstall":
		return serviceControl("uninstall", actionArgs, cat)
	case "start":
		return serviceControl("start", actionArgs, cat)
	case "stop":
		return serviceControl("stop", actionArgs, cat)
	case "status":
		return serviceStatus(actionArgs, cat)
	default:
		fmt.Fprintf(os.Stderr, "proxydge service: %s\n", cat.T("error.unknown_command", action))
		return 2
	}
}

// serviceInstall handles "proxydge service install [-config <path>] [-lang <lang>]".
func serviceInstall(args []string, cat *i18n.Catalog) int {
	fs := flag.NewFlagSet("proxydge service install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFlag := fs.String("config", "", "config file path")
	lang := fs.String("lang", "", "display language")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *lang != "" {
		cat, _ = i18n.Load(i18n.DetectLocale(*lang))
	}

	cfgPath := *configFlag
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}

	absPath, err := filepath.Abs(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		return 1
	}

	if _, err := os.Stat(absPath); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.config_not_found", absPath))
		return 2
	}

	ctrl, err := newServiceControllerFunc(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "install", err))
		return 1
	}

	// Idempotent: already installed → print notice and exit.
	status, err := ctrl.Status()
	if err == nil && status != service.StatusUnknown {
		fmt.Fprintln(os.Stderr, cat.T("service.already_installed"))
		return 0
	}

	if err := ctrl.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "install", err))
		return 1
	}
	fmt.Fprintln(os.Stderr, cat.T("service.installed"))

	if err := ctrl.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "start", err))
		return 1
	}
	fmt.Fprintln(os.Stderr, cat.T("service.started"))
	return 0
}

// serviceControl handles "proxydge service <start|stop|uninstall> [-lang <lang>]".
func serviceControl(action string, args []string, cat *i18n.Catalog) int {
	fs := flag.NewFlagSet("proxydge service "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	lang := fs.String("lang", "", "display language")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *lang != "" {
		cat, _ = i18n.Load(i18n.DetectLocale(*lang))
	}

	ctrl, err := newServiceControllerFunc(config.DefaultConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, err))
		return 1
	}

	var opErr error
	switch action {
	case "start":
		opErr = ctrl.Start()
	case "stop":
		opErr = ctrl.Stop()
	case "uninstall":
		opErr = ctrl.Uninstall()
	}
	if opErr != nil {
		if errors.Is(opErr, service.ErrNotInstalled) {
			fmt.Fprintln(os.Stderr, cat.T("service.not_installed"))
			return 2
		}
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, opErr))
		return 1
	}

	switch action {
	case "start":
		fmt.Fprintln(os.Stderr, cat.T("service.started"))
	case "stop":
		fmt.Fprintln(os.Stderr, cat.T("service.stopped"))
	case "uninstall":
		fmt.Fprintln(os.Stderr, cat.T("service.uninstalled"))
	}
	return 0
}

// serviceStatus handles "proxydge service status [-lang <lang>]".
func serviceStatus(args []string, cat *i18n.Catalog) int {
	fs := flag.NewFlagSet("proxydge service status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	lang := fs.String("lang", "", "display language")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *lang != "" {
		cat, _ = i18n.Load(i18n.DetectLocale(*lang))
	}

	ctrl, err := newServiceControllerFunc(config.DefaultConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "status", err))
		return 1
	}

	status, err := ctrl.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "status", err))
		return 1
	}

	switch status {
	case service.StatusRunning:
		fmt.Fprintln(os.Stderr, cat.T("service.status.running"))
	case service.StatusStopped:
		fmt.Fprintln(os.Stderr, cat.T("service.status.stopped"))
	default:
		fmt.Fprintln(os.Stderr, cat.T("service.status.unknown"))
	}
	return 0
}
