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

// ServiceController abstracts the service lifecycle operations so that
// cmdService can be tested without importing kardianos/service directly.
type ServiceController interface {
	Install() error
	Uninstall() error
	Start() error
	Stop() error
	Status() (service.Status, error)
}

// kardianosServiceController wraps a kardianos service.Service.
type kardianosServiceController struct {
	svc service.Service
}

func (c *kardianosServiceController) Install() error                       { return c.svc.Install() }
func (c *kardianosServiceController) Uninstall() error                     { return c.svc.Uninstall() }
func (c *kardianosServiceController) Start() error                        { return c.svc.Start() }
func (c *kardianosServiceController) Stop() error                         { return c.svc.Stop() }
func (c *kardianosServiceController) Status() (service.Status, error)     { return c.svc.Status() }

// newServiceControllerFunc is the factory used by service commands to create
// a ServiceController. Tests replace this with a fake.
var newServiceControllerFunc = newServiceController

// newServiceController creates the kardianos service.Service with the given
// config path recorded in the service arguments. The returned
// ServiceController is ready for Install/Uninstall/Start/Stop/Status.
// configPath must be an absolute path.
func newServiceController(configPath string) (ServiceController, error) {
	svcConfig := &service.Config{
		Name:        "ProxyDge",
		DisplayName: "ProxyDge Gateway",
		Description: "PROXY Protocol normalizing gateway for TCP and UDP",
		Arguments:   []string{"start", "-config", configPath},
		Dependencies: []string{
			"Requires=network.target",
			"After=network.target",
		},
		Option: service.KeyValue{
			"DelayedAutoStart": true,
			"OnFailure":        "restart",
			"Restart":          "always",
			"SuccessExitStatus": "0",
		},
	}

	// serviceNoOp is a no-op shell for install/control operations.
	// The actual gateway runs when the OS service manager starts the binary
	// with the recorded arguments, hitting cmdStart via the normal run() path.
	svc, err := service.New(&serviceNoOp{}, svcConfig)
	if err != nil {
		return nil, err
	}
	return &kardianosServiceController{svc: svc}, nil
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

	// Resolve locale if not already resolved.
	if *lang != "" {
		cat, _ = i18n.Load(i18n.DetectLocale(*lang))
	}

	// Resolve config path: explicit flag > default exe-dir.
	cfgPath := *configFlag
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}

	// Convert to absolute path — service processes may have a different
	// working directory than the interactive terminal.
	absPath, err := filepath.Abs(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %v\n", err)
		return 1
	}

	// Check config file exists before installing.
	if _, err := os.Stat(absPath); err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.config_not_found", absPath))
		return 2
	}

	ctrl, err := newServiceControllerFunc(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", "install", err))
		return 1
	}

	// Check if already installed (ddns-go pattern).
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

	// Auto-start after install.
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

	// For start/stop/uninstall, config path doesn't matter — the service
	// already has its arguments recorded. Use default for the controller.
	ctrl, err := newServiceControllerFunc(config.DefaultConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, err))
		return 1
	}

	switch action {
	case "start":
		if err := ctrl.Start(); err != nil {
			if errors.Is(err, service.ErrNotInstalled) {
				fmt.Fprintln(os.Stderr, cat.T("service.not_installed"))
				return 2
			}
			fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, err))
			return 1
		}
		fmt.Fprintln(os.Stderr, cat.T("service.started"))
	case "stop":
		if err := ctrl.Stop(); err != nil {
			if errors.Is(err, service.ErrNotInstalled) {
				fmt.Fprintln(os.Stderr, cat.T("service.not_installed"))
				return 2
			}
			fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, err))
			return 1
		}
		fmt.Fprintln(os.Stderr, cat.T("service.stopped"))
	case "uninstall":
		if err := ctrl.Uninstall(); err != nil {
			if errors.Is(err, service.ErrNotInstalled) {
				fmt.Fprintln(os.Stderr, cat.T("service.not_installed"))
				return 2
			}
			fmt.Fprintf(os.Stderr, "proxydge: %s\n", cat.T("error.service_action", action, err))
			return 1
		}
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
