package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kardianos/service"

	"proxydge/internal/i18n"
)

// fakeServiceController implements service.Service for testing.
type fakeServiceController struct {
	installErr   error
	uninstallErr error
	startErr     error
	stopErr      error
	statusVal    service.Status
	statusErr    error

	installCalled   bool
	uninstallCalled bool
	startCalled     bool
	stopCalled      bool
}

func (f *fakeServiceController) Install() error                      { f.installCalled = true; return f.installErr }
func (f *fakeServiceController) Uninstall() error                    { f.uninstallCalled = true; return f.uninstallErr }
func (f *fakeServiceController) Start() error                        { f.startCalled = true; return f.startErr }
func (f *fakeServiceController) Stop() error                         { f.stopCalled = true; return f.stopErr }
func (f *fakeServiceController) Status() (service.Status, error)     { return f.statusVal, f.statusErr }
func (f *fakeServiceController) Restart() error                      { return nil }
func (f *fakeServiceController) Run() error                          { return nil }
func (f *fakeServiceController) Logger(errs chan<- error) (service.Logger, error) { return nil, nil }
func (f *fakeServiceController) SystemLogger(errs chan<- error) (service.Logger, error) { return nil, nil }
func (f *fakeServiceController) String() string                      { return "fake" }
func (f *fakeServiceController) Platform() string                    { return "fake" }

// captureStderr runs fn and returns what was written to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = orig
	b, _ := io.ReadAll(r)
	return string(b)
}

// cat returns an English catalog for tests.
func testCat() *i18n.Catalog {
	c, _ := i18n.Load(i18n.LocaleEN)
	return c
}

// --- cmdService dispatch ---

func TestCmdServiceUnknownAction(t *testing.T) {
	if code := cmdService([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown action: want 2, got %d", code)
	}
}

func TestCmdServiceNoAction(t *testing.T) {
	if code := cmdService(nil); code != 2 {
		t.Fatalf("no action: want 2, got %d", code)
	}
}

// --- serviceInstall ---

func TestServiceInstallMissingConfig(t *testing.T) {
	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceInstall([]string{"-config", "/nonexistent/config.yaml"}, cat)
		if code != 2 {
			t.Fatalf("want exit 2, got %d", code)
		}
	})
	if !strings.Contains(got, "config file not found") {
		t.Fatalf("expected 'config file not found' in stderr, got: %s", got)
	}
}

func TestServiceInstallAlreadyInstalled(t *testing.T) {
	fake := &fakeServiceController{statusVal: service.StatusRunning}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfgPath, []byte("version: 3\n"), 0o644)

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceInstall([]string{"-config", cfgPath}, cat)
		if code != 0 {
			t.Fatalf("already installed: want 0, got %d", code)
		}
	})
	if !strings.Contains(got, "already installed") {
		t.Fatalf("expected 'already installed' in stderr, got: %s", got)
	}
	if fake.installCalled {
		t.Fatal("Install should not be called when already installed")
	}
}

func TestServiceInstallFresh(t *testing.T) {
	fake := &fakeServiceController{statusVal: service.StatusUnknown}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	// Create a real temp config file so the existence check passes.
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	os.WriteFile(cfgPath, []byte("version: 3\nupstream: 127.0.0.1:1\n"), 0o644)

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceInstall([]string{"-config", cfgPath}, cat)
		if code != 0 {
			t.Fatalf("fresh install: want 0, got %d", code)
		}
	})
	if !fake.installCalled {
		t.Fatal("Install should have been called")
	}
	if !fake.startCalled {
		t.Fatal("Start should have been called after install")
	}
	if !strings.Contains(got, "installed") {
		t.Fatalf("expected 'installed' in stderr, got: %s", got)
	}
	if !strings.Contains(got, "started") {
		t.Fatalf("expected 'started' in stderr, got: %s", got)
	}
}

// --- serviceControl ---

func TestServiceControlStartOK(t *testing.T) {
	fake := &fakeServiceController{}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceControl("start", nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !fake.startCalled {
		t.Fatal("Start should have been called")
	}
	if !strings.Contains(got, "started") {
		t.Fatalf("expected 'started' in stderr, got: %s", got)
	}
}

func TestServiceControlStopOK(t *testing.T) {
	fake := &fakeServiceController{}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceControl("stop", nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !fake.stopCalled {
		t.Fatal("Stop should have been called")
	}
	if !strings.Contains(got, "stopped") {
		t.Fatalf("expected 'stopped' in stderr, got: %s", got)
	}
}

func TestServiceControlUninstallOK(t *testing.T) {
	fake := &fakeServiceController{}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceControl("uninstall", nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !fake.uninstallCalled {
		t.Fatal("Uninstall should have been called")
	}
	if !strings.Contains(got, "uninstalled") {
		t.Fatalf("expected 'uninstalled' in stderr, got: %s", got)
	}
}

func TestServiceControlNotInstalled(t *testing.T) {
	for _, action := range []string{"start", "stop", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			fake := &fakeServiceController{
				startErr:     service.ErrNotInstalled,
				stopErr:      service.ErrNotInstalled,
				uninstallErr: service.ErrNotInstalled,
			}
			orig := newServiceControllerFunc
			newServiceControllerFunc = func(configPath string) (service.Service, error) {
				return fake, nil
			}
			defer func() { newServiceControllerFunc = orig }()

			cat := testCat()
			got := captureStderr(t, func() {
				code := serviceControl(action, nil, cat)
				if code != 2 {
					t.Fatalf("want 2, got %d", code)
				}
			})
			if !strings.Contains(got, "not installed") {
				t.Fatalf("expected 'not installed' in stderr, got: %s", got)
			}
		})
	}
}

func TestServiceControlGenericError(t *testing.T) {
	fake := &fakeServiceController{startErr: errors.New("access denied")}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceControl("start", nil, cat)
		if code != 1 {
			t.Fatalf("want 1, got %d", code)
		}
	})
	if !strings.Contains(got, "access denied") {
		t.Fatalf("expected error message in stderr, got: %s", got)
	}
}

// --- serviceStatus ---

func TestServiceStatusRunning(t *testing.T) {
	fake := &fakeServiceController{statusVal: service.StatusRunning}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceStatus(nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !strings.Contains(got, "Running") {
		t.Fatalf("expected 'Running', got: %s", got)
	}
}

func TestServiceStatusStopped(t *testing.T) {
	fake := &fakeServiceController{statusVal: service.StatusStopped}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceStatus(nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !strings.Contains(got, "Stopped") {
		t.Fatalf("expected 'Stopped', got: %s", got)
	}
}

func TestServiceStatusUnknown(t *testing.T) {
	fake := &fakeServiceController{statusVal: service.StatusUnknown}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceStatus(nil, cat)
		if code != 0 {
			t.Fatalf("want 0, got %d", code)
		}
	})
	if !strings.Contains(got, "Unknown") {
		t.Fatalf("expected 'Unknown', got: %s", got)
	}
}

func TestServiceStatusError(t *testing.T) {
	fake := &fakeServiceController{statusErr: errors.New("permission denied")}
	orig := newServiceControllerFunc
	newServiceControllerFunc = func(configPath string) (service.Service, error) {
		return fake, nil
	}
	defer func() { newServiceControllerFunc = orig }()

	cat := testCat()
	got := captureStderr(t, func() {
		code := serviceStatus(nil, cat)
		if code != 1 {
			t.Fatalf("want 1, got %d", code)
		}
	})
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("expected error in stderr, got: %s", got)
	}
}
