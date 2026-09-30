package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeServiceControl struct {
	mu        sync.Mutex
	info      ServiceInfo
	err       error
	calls     []string
	autoStart []bool
}

func (f *fakeServiceControl) Info() ServiceInfo { f.mu.Lock(); defer f.mu.Unlock(); return f.info }
func (f *fakeServiceControl) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.err
}
func (f *fakeServiceControl) InstallElevated(context.Context) error   { return f.record("install") }
func (f *fakeServiceControl) UninstallElevated(context.Context) error { return f.record("uninstall") }
func (f *fakeServiceControl) UninstallSelf() error                    { return f.record("uninstall-self") }
func (f *fakeServiceControl) Start() error                            { return f.record("start") }
func (f *fakeServiceControl) SetAutoStartElevated(_ context.Context, enabled bool) error {
	f.mu.Lock()
	f.autoStart = append(f.autoStart, enabled)
	f.mu.Unlock()
	return f.record("autostart")
}
func (f *fakeServiceControl) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func serviceTestApp(t *testing.T, info ServiceInfo) (*App, *fakeServiceControl) {
	t.Helper()
	a := newTestApp(t)
	fake := &fakeServiceControl{info: info}
	a.service = fake
	return a, fake
}

func serveJSON(t *testing.T, a *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("{}")
	}
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, method, path, reader))
	return rr
}

func apiCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body APIError
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return body.Code
}

func TestPanelStatusReportsServiceAndVersion(t *testing.T) {
	a, _ := serviceTestApp(t, ServiceInfo{Supported: true, Name: serviceName, Installed: true, State: "running", AutoStart: true, RunningAsService: true})
	rr := serveJSON(t, a, http.MethodGet, "/api/panel", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/panel = %d %s", rr.Code, rr.Body)
	}
	var got panelInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != version || got.Listen != "127.0.0.1:8787" || !got.RunningAsService || !got.Service.Installed || !got.Service.AutoStart || got.Root == "" {
		t.Fatalf("panel info = %+v", got)
	}
}

func TestInstallServiceHandsOverToTheService(t *testing.T) {
	a, fake := serviceTestApp(t, ServiceInfo{Supported: true})
	stopped := make(chan struct{})
	a.lifecycle.bind(func() { close(stopped) })
	rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/install", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("install = %d %s", rr.Code, rr.Body)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the console panel did not stop after installing the service")
	}
	if err := a.lifecycle.runAfterStop(); err != nil {
		t.Fatal(err)
	}
	if calls := fake.Calls(); strings.Join(calls, ",") != "install,start" {
		t.Fatalf("calls = %v, want install then start after the panel stopped", calls)
	}
}

func TestInstallServiceIsRefusedInsideTheServiceAndOffWindows(t *testing.T) {
	for _, info := range []ServiceInfo{{Supported: false}, {Supported: true, Installed: true, RunningAsService: true}} {
		a, fake := serviceTestApp(t, info)
		rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/install", "")
		if rr.Code != http.StatusConflict || apiCode(t, rr) != "service_unavailable" || len(fake.Calls()) != 0 {
			t.Fatalf("info=%+v install = %d %s calls=%v", info, rr.Code, rr.Body, fake.Calls())
		}
	}
}

func TestInstallServiceReportsDeclinedPrompt(t *testing.T) {
	a, fake := serviceTestApp(t, ServiceInfo{Supported: true})
	fake.err = errElevationCanceled
	stopped := make(chan struct{}, 1)
	a.lifecycle.bind(func() { stopped <- struct{}{} })
	rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/install", "")
	if rr.Code != http.StatusConflict || apiCode(t, rr) != "elevation_canceled" {
		t.Fatalf("install = %d %s", rr.Code, rr.Body)
	}
	select {
	case <-stopped:
		t.Fatal("a failed install must keep the console panel running")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestUninstallServiceFromConsoleAndFromTheService(t *testing.T) {
	a, fake := serviceTestApp(t, ServiceInfo{Supported: true, Installed: true})
	if rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/uninstall", ""); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"stopping":true`) {
		t.Fatalf("console uninstall = %d %s", rr.Code, rr.Body)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0] != "uninstall" {
		t.Fatalf("console calls = %v", calls)
	}

	a, fake = serviceTestApp(t, ServiceInfo{Supported: true, Installed: true, RunningAsService: true})
	stopped := make(chan struct{})
	a.lifecycle.bind(func() { close(stopped) })
	if rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/uninstall", ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"stopping":true`) {
		t.Fatalf("service uninstall = %d %s", rr.Code, rr.Body)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the service must stop after removing its registration")
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0] != "uninstall-self" {
		t.Fatalf("service calls = %v", calls)
	}

	a, fake = serviceTestApp(t, ServiceInfo{Supported: true})
	if rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/uninstall", ""); rr.Code != http.StatusConflict || apiCode(t, rr) != "service_not_installed" || len(fake.Calls()) != 0 {
		t.Fatalf("uninstall without service = %d %s", rr.Code, rr.Body)
	}
}

func TestServiceAutoStartNeedsConsoleAndInstalledService(t *testing.T) {
	a, fake := serviceTestApp(t, ServiceInfo{Supported: true, Installed: true})
	if rr := serveJSON(t, a, http.MethodPut, "/api/panel/service/autostart", `{"enabled":true}`); rr.Code != http.StatusOK {
		t.Fatalf("autostart = %d %s", rr.Code, rr.Body)
	}
	if len(fake.autoStart) != 1 || !fake.autoStart[0] {
		t.Fatalf("autostart calls = %v", fake.autoStart)
	}
	a, fake = serviceTestApp(t, ServiceInfo{Supported: true, Installed: true, RunningAsService: true})
	if rr := serveJSON(t, a, http.MethodPut, "/api/panel/service/autostart", `{"enabled":true}`); rr.Code != http.StatusConflict || apiCode(t, rr) != "service_elevation_unavailable" || len(fake.Calls()) != 0 {
		t.Fatalf("autostart inside the service = %d %s", rr.Code, rr.Body)
	}
	a, _ = serviceTestApp(t, ServiceInfo{Supported: true})
	if rr := serveJSON(t, a, http.MethodPut, "/api/panel/service/autostart", `{"enabled":false}`); rr.Code != http.StatusConflict || apiCode(t, rr) != "service_not_installed" {
		t.Fatalf("autostart without service = %d %s", rr.Code, rr.Body)
	}
}

func TestServiceFailureIsSanitized(t *testing.T) {
	a, fake := serviceTestApp(t, ServiceInfo{Supported: true})
	fake.err = errors.New("access denied password=hunter2")
	rr := serveJSON(t, a, http.MethodPost, "/api/panel/service/install", "")
	if rr.Code != http.StatusInternalServerError || apiCode(t, rr) != "service_failed" || strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatalf("install failure = %d %s", rr.Code, rr.Body)
	}
}

func TestStopPanelStopsServe(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.Addr().String()
	probe.Close()
	a := newTestAppAt(t, listen)
	done := make(chan error, 1)
	go func() { done <- a.Serve(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.lifecycle.mu.Lock()
		bound := a.lifecycle.stop != nil
		a.lifecycle.mu.Unlock()
		if bound || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	req := saveRequest(a, http.MethodPost, "/api/panel/stop", strings.NewReader("{}"))
	req.Host = listen
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("stop = %d %s", rr.Code, rr.Body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after /api/panel/stop")
	}
}
