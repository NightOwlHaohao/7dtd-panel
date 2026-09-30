package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"
)

const serviceName = "7DTDserverPanel"

const serviceAccount = `NT SERVICE\` + serviceName

var (
	errServiceUnsupported = errors.New("the Windows service is available on Windows only")
	errElevationCanceled  = errors.New("the administrator prompt was declined")
)

// ServiceInfo describes the panel's Windows service registration as seen
// by the running panel.
type ServiceInfo struct {
	Supported        bool   `json:"supported"`
	Name             string `json:"name"`
	Account          string `json:"account"`
	Installed        bool   `json:"installed"`
	State            string `json:"state,omitempty"` // stopped, start_pending, running, stop_pending
	AutoStart        bool   `json:"autoStart"`       // starts with Windows (delayed automatic)
	ExePath          string `json:"exePath,omitempty"`
	CurrentExe       bool   `json:"currentExe"`       // the registration points at this panel.exe
	RunningAsService bool   `json:"runningAsService"` // this process is the service
	Error            string `json:"error,omitempty"`
}

// serviceControl manages the service registration. Install, uninstall and
// the start type need administrator rights: from an interactive session they
// show a UAC prompt; the service itself can only remove its own registration.
type serviceControl interface {
	Info() ServiceInfo
	InstallElevated(ctx context.Context) error
	UninstallElevated(ctx context.Context) error
	SetAutoStartElevated(ctx context.Context, enabled bool) error
	UninstallSelf() error
	Start() error
}

type unsupportedServiceControl struct{}

func (unsupportedServiceControl) Info() ServiceInfo {
	return ServiceInfo{Name: serviceName, Account: serviceAccount}
}
func (unsupportedServiceControl) InstallElevated(context.Context) error { return errServiceUnsupported }
func (unsupportedServiceControl) UninstallElevated(context.Context) error {
	return errServiceUnsupported
}
func (unsupportedServiceControl) SetAutoStartElevated(context.Context, bool) error {
	return errServiceUnsupported
}
func (unsupportedServiceControl) UninstallSelf() error { return errServiceUnsupported }
func (unsupportedServiceControl) Start() error         { return errServiceUnsupported }

type panelInfo struct {
	Version          string      `json:"version"`
	Listen           string      `json:"listen"`
	Executable       string      `json:"executable"`
	Root             string      `json:"root"`
	RunningAsService bool        `json:"runningAsService"`
	Elevated         bool        `json:"elevated"`
	Service          ServiceInfo `json:"service"`
}

// panelLifecycle lets handlers stop the panel after their response has been
// sent, optionally running a follow-up (such as starting the service) once
// the HTTP server has released the listen address.
type panelLifecycle struct {
	mu    sync.Mutex
	stop  context.CancelFunc
	after func() error
}

func (l *panelLifecycle) bind(stop context.CancelFunc) {
	l.mu.Lock()
	l.stop = stop
	l.mu.Unlock()
}

// requestStop stops the panel shortly after the current response is written.
func (l *panelLifecycle) requestStop(after func() error) {
	l.mu.Lock()
	stop := l.stop
	if after != nil {
		l.after = after
	}
	l.mu.Unlock()
	if stop != nil {
		time.AfterFunc(300*time.Millisecond, stop)
	}
}

// runAfterStop runs the follow-up registered by requestStop, if any.
func (l *panelLifecycle) runAfterStop() error {
	l.mu.Lock()
	after := l.after
	l.after = nil
	l.mu.Unlock()
	if after == nil {
		return nil
	}
	return after()
}

// preparePanelFolders creates the folders the panel writes to and panel.json,
// so a new installation can be registered as a service before first use.
func preparePanelFolders(paths Paths) error {
	if err := paths.PrepareDataDirs(); err != nil {
		return err
	}
	_, err := LoadPanelConfig(paths.PanelConfig) // creates panel.json when missing
	return err
}

func (a *App) panelStatus(w http.ResponseWriter, _ *http.Request) {
	exe, _ := os.Executable()
	info := a.service.Info()
	elevated := a.firewallAdmin != nil && a.firewallAdmin()
	a.json(w, http.StatusOK, panelInfo{Version: version, Listen: a.cfg.Listen, Executable: exe, Root: a.paths.Root, RunningAsService: info.RunningAsService, Elevated: elevated, Service: info})
}

// installService registers (or updates) the service for this panel.exe and
// then hands over: this interactive panel stops and the service starts.
func (a *App) installService(w http.ResponseWriter, req *http.Request) {
	info := a.service.Info()
	if !info.Supported || info.RunningAsService {
		a.error(w, http.StatusConflict, "service_unavailable", "service installation must be started from a panel that is not running as the service")
		return
	}
	if !a.auditAttempt(w, "service install") {
		return
	}
	err := a.service.InstallElevated(req.Context())
	if auditErr := a.auditResult("service install", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.serviceError(w, err)
		return
	}
	a.json(w, http.StatusAccepted, map[string]bool{"switching": true})
	a.lifecycle.requestStop(a.service.Start)
}

func (a *App) uninstallService(w http.ResponseWriter, req *http.Request) {
	info := a.service.Info()
	if !info.Supported || !info.Installed {
		a.error(w, http.StatusConflict, "service_not_installed", "the service is not installed")
		return
	}
	if !a.auditAttempt(w, "service uninstall") {
		return
	}
	var err error
	if info.RunningAsService {
		err = a.service.UninstallSelf()
	} else {
		err = a.service.UninstallElevated(req.Context())
	}
	if auditErr := a.auditResult("service uninstall", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.serviceError(w, err)
		return
	}
	// A service that removed its own registration stops so the SCM can delete it.
	a.json(w, http.StatusOK, map[string]bool{"stopping": info.RunningAsService})
	if info.RunningAsService {
		a.lifecycle.requestStop(nil)
	}
}

// setServiceAutoStart switches the service between manual and delayed
// automatic start. It needs a UAC prompt, which the service itself cannot
// show, so it is only available from a panel running in a console window.
func (a *App) setServiceAutoStart(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	info := a.service.Info()
	if !info.Supported || !info.Installed {
		a.error(w, http.StatusConflict, "service_not_installed", "the service is not installed")
		return
	}
	if info.RunningAsService {
		a.error(w, http.StatusConflict, "service_elevation_unavailable", "the service cannot show an administrator prompt")
		return
	}
	action := "service autostart off"
	if body.Enabled {
		action = "service autostart on"
	}
	if !a.auditAttempt(w, action) {
		return
	}
	err := a.service.SetAutoStartElevated(req.Context(), body.Enabled)
	if auditErr := a.auditResult(action, err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.serviceError(w, err)
		return
	}
	a.json(w, http.StatusOK, a.service.Info())
}

func (a *App) stopPanel(w http.ResponseWriter, _ *http.Request) {
	if !a.auditAttempt(w, "panel stop") {
		return
	}
	_ = a.auditResult("panel stop", nil)
	a.json(w, http.StatusAccepted, map[string]bool{"stopping": true})
	a.lifecycle.requestStop(nil)
}

func (a *App) serviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errElevationCanceled):
		a.error(w, http.StatusConflict, "elevation_canceled", "the administrator prompt was declined")
	case errors.Is(err, errServiceUnsupported):
		a.error(w, http.StatusConflict, "service_unavailable", err.Error())
	default:
		a.error(w, http.StatusInternalServerError, "service_failed", sanitizedError(err))
	}
}
