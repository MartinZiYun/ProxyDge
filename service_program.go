package main

import (
	"fmt"
	"os"
	"time"

	"github.com/kardianos/service"

	"proxydge/internal/config"
)

// serviceNoOp is a minimal service.Interface used for install/control
// operations where the OS service manager never actually calls Start/Stop
// (Install, Uninstall, Start, Stop, Status are control-plane operations).
type serviceNoOp struct{}

func (s *serviceNoOp) Start(svc service.Service) error { return nil }
func (s *serviceNoOp) Stop(svc service.Service) error  { return nil }

// proxydgeService implements service.Interface for running ProxyDge as a
// system service. The lifecycle is:
//
//	service.Run()
//	  → Start(): call runGateway(cfg), non-blocking
//	  → ...gateway running...
//	  → Stop(): closer(), wait done, return
//
// On fatal gateway error: os.Exit(1) to trigger OS Recovery Action.
//
// errc is single-consumer: only the monitor goroutine reads from it.
// Stop() waits on `done` (closed after errc is consumed).
type proxydgeService struct {
	cfg    *config.Config
	closer func()
	errc   <-chan error
	done   chan struct{} // closed after errc is consumed
}

func (p *proxydgeService) Start(s service.Service) error {
	closer, errc, err := runGateway(p.cfg)
	if err != nil {
		// Fatal: cannot start gateway. Exit immediately so the OS
		// service manager detects the failure and runs recovery.
		fmt.Fprintf(os.Stderr, "proxydge: fatal: %v\n", err)
		os.Exit(1)
	}
	p.closer = closer
	p.errc = errc
	p.done = make(chan struct{})

	// Single consumer for errc. Either:
	//  - errc returns nil (normal shutdown triggered by Stop()) → close done
	//  - errc returns error (fatal gateway error) → os.Exit(1)
	go func() {
		err := <-errc
		close(p.done)
		if err != nil {
			fmt.Fprintf(os.Stderr, "proxydge: fatal: gateway error: %v\n", err)
			os.Exit(1)
		}
	}()

	return nil
}

func (p *proxydgeService) Stop(s service.Service) error {
	p.closer()
	// Wait for the monitor goroutine to consume errc and close done.
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
	return nil
}
