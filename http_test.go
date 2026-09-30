package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestApp(t *testing.T) *App {
	return newTestAppAt(t, "127.0.0.1:8787")
}

func newTestAppAt(t *testing.T, listen string) *App {
	t.Helper()
	a, err := NewApp(ResolvePaths(t.TempDir()), PanelConfig{Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEmbeddedAssetsAreNotCachedAcrossExeUpdates(t *testing.T) {
	a := newTestApp(t)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/js/main.js", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET app.js = %d", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}
}

func TestPerformanceRouteReturnsPartialSample(t *testing.T) {
	a := newTestApp(t)
	want := PerformanceSample{HostCPU: MetricValue{Available: true, Value: 37}, HostGPU: unavailableMetric("GPU collector unavailable")}
	a.performanceSample = func(context.Context) (PerformanceSample, error) { return want, nil }
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/performance", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET performance = %d: %s", rr.Code, rr.Body.String())
	}
	var got PerformanceSample
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.HostCPU.Available || got.HostCPU.Value != want.HostCPU.Value || got.HostGPU.Available {
		t.Fatalf("performance = %#v", got)
	}
}

func TestPerformanceRouteSanitizesUnavailableError(t *testing.T) {
	a := newTestApp(t)
	a.performanceSample = func(context.Context) (PerformanceSample, error) {
		return unavailableSample(`C:\\private\\panel.exe missing`)
	}
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/performance", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET performance = %d: %s", rr.Code, rr.Body.String())
	}
	var got APIError
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "performance_unavailable" || strings.Contains(rr.Body.String(), `C:\\private`) {
		t.Fatalf("unavailable response = %#v", got)
	}
}

func saveRequest(a *App, method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, body)
	req.RemoteAddr = "127.0.0.1:51000"
	if method != http.MethodGet {
		req.Header.Set("X-Panel-Token", a.token)
	}
	return req
}

func TestSaveRoutes(t *testing.T) {
	a := newTestApp(t)
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/saves"},
		{http.MethodPost, "/api/saves/switch"},
		{http.MethodPost, "/api/saves/export"},
		{http.MethodPost, "/api/saves/import"},
		{http.MethodDelete, "/api/saves/Navezgane/game"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, saveRequest(a, test.method, test.path, nil))
			if rr.Code == http.StatusNotFound {
				t.Fatalf("route returned 404: %s", rr.Body.String())
			}
		})
	}
}

func TestSaveCatalogRoute(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, filepath.Join(a.paths.BuiltinWorlds, "Navezgane", "world.xml"), "world")
	writeFile(t, filepath.Join(a.paths.Saves, "Navezgane", "game", "main.ttw"), "save")
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="GameWorld" value="Navezgane"/><property name="GameName" value="game"/></ServerSettings>`)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/saves", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET saves = %d: %s", rr.Code, rr.Body.String())
	}
	var catalog SaveCatalog
	if err := json.NewDecoder(rr.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Worlds) != 1 || len(catalog.Worlds[0].Saves) != 1 || !catalog.Worlds[0].Saves[0].Active {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestSaveMutationRejectsRunningServerBeforeFilesChange(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, filepath.Join(a.paths.Saves, "Navezgane", "game", "main.ttw"), "save")
	started := time.Unix(10, 0)
	a.server = newServerManager(a.paths, a.events, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: a.paths.ServerExe, Started: started}}, nil)
	a.saves.server = a.server
	writeRecordedProcess(t, a.paths, persistedProcess{PID: 1, Exe: a.paths.ServerExe, Started: started})
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/saves/export", strings.NewReader(`{"world":"Navezgane","game":"game"}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("POST export = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(a.paths.SavePackages); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("export changed files: %v", err)
	}
}

func TestSaveImportRejectsRunningServerBeforeUploadFilesChange(t *testing.T) {
	a := newTestApp(t)
	started := time.Unix(10, 0)
	a.server = newServerManager(a.paths, a.events, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: a.paths.ServerExe, Started: started}}, nil)
	a.saves.server = a.server
	writeRecordedProcess(t, a.paths, persistedProcess{PID: 1, Exe: a.paths.ServerExe, Started: started})

	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "imported"}, map[string]string{"save/main.ttw": "save"})
	data := new(bytes.Buffer)
	form := multipart.NewWriter(data)
	part, err := form.CreateFormFile("package", "save.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/saves/import", data)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(a.paths.SavePackages); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import created upload files: %v", err)
	}
}

func TestSaveImportRejectsReparsePackageDirectoryBeforeWriting(t *testing.T) {
	a, outside := newTestApp(t), t.TempDir()
	if err := os.MkdirAll(a.paths.Backups, 0700); err != nil {
		t.Fatal(err)
	}
	makeReparseLink(t, outside, a.paths.SavePackages)
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "imported"}, map[string]string{"save/main.ttw": "save"})
	data := new(bytes.Buffer)
	form := multipart.NewWriter(data)
	part, err := form.CreateFormFile("package", "save.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/saves/import", data)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v err=%v", entries, err)
	}
}

func TestSaveImportClassifiesMalformedAndOversizedRequests(t *testing.T) {
	for name, test := range map[string]struct {
		body io.Reader
		want int
	}{
		"malformed": {body: strings.NewReader("not multipart"), want: http.StatusBadRequest},
		"oversized": {body: maxBytesErrorReader{}, want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			req := saveRequest(a, http.MethodPost, "/api/saves/import", test.body)
			req.Header.Set("Content-Type", "multipart/form-data; boundary=panel")
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, req)
			if rr.Code != test.want {
				t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
			}
			release, err := a.server.BeginStoppedOperation()
			if err != nil {
				t.Fatalf("import retained stopped-operation lease: %v", err)
			}
			release()
		})
	}
}

type maxBytesErrorReader struct{}

func (maxBytesErrorReader) Read([]byte) (int, error) { return 0, &http.MaxBytesError{Limit: 64 << 30} }

func TestSaveMutationImportsPackageAndRequiresDeleteConfirmation(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(a.paths.UserData, 0700); err != nil {
		t.Fatal(err)
	}
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "imported"}, map[string]string{"save/main.ttw": "save"})
	data := new(bytes.Buffer)
	form := multipart.NewWriter(data)
	part, err := form.CreateFormFile("package", "save.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/saves/import", data)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
	}
	if got := string(mustRead(t, filepath.Join(a.paths.Saves, "Navezgane", "imported", "main.ttw"))); got != "save" {
		t.Fatalf("imported save = %q", got)
	}
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodDelete, "/api/saves/Navezgane/imported", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("DELETE without confirm = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUnknownMutationReturnsJSONAPIError(t *testing.T) {
	a := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/missing", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d", rr.Code)
	}
	var apiErr APIError
	if err := json.NewDecoder(rr.Body).Decode(&apiErr); err != nil || apiErr.Code != "not_found" {
		t.Fatalf("got %q: %v", rr.Body.String(), err)
	}
}

func TestMutationRequiresSameOriginAndToken(t *testing.T) {
	a := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/setup/prepare", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestModsHTTPReadAndWriteBoundaries(t *testing.T) {
	a := newTestApp(t)
	writeModInfo(t, filepath.Join(a.paths.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/mods", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"mods"`) {
		t.Fatalf("GET=%d %s", rr.Code, rr.Body.String())
	}
	for _, path := range []string{"/api/mods/Core", "/api/mods/policy"} {
		rr = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8787"+path, strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:51000"
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("unauthenticated %s=%d", path, rr.Code)
		}
	}
	for _, path := range []string{"/api/mods/%2e%2e", "/api/mods/a%2fb", "/api/mods/a%5cb"} {
		rr = httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPut, path, strings.NewReader(`{"enabled":false}`)))
		if rr.Code != http.StatusBadRequest || strings.Contains(rr.Body.String(), a.paths.Root) {
			t.Fatalf("unsafe %s=%d %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestModsUploadStagesZIPAndReturnsCatalog(t *testing.T) {
	a := newTestApp(t)
	if err := os.Mkdir(a.paths.ServerDir, 0700); err != nil {
		t.Fatal(err)
	}
	archive := makeModPackage(t, map[string]string{
		"Addon/":            "",
		"Addon/ModInfo.xml": `<xml><ModInfo><ID value="addon"/><Name value="Addon"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`,
		"Addon/payload.txt": "payload",
	})
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, modUploadRequest(t, a, archive, "addon.zip"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"id":"addon"`) {
		t.Fatalf("POST upload=%d %s", rr.Code, rr.Body.String())
	}
	if got := string(mustRead(t, filepath.Join(a.paths.Mods, "Addon", "payload.txt"))); got != "payload" {
		t.Fatalf("payload=%q", got)
	}
}

func TestModsUploadRejectsUnsafeConflictingAndActiveRequests(t *testing.T) {
	t.Run("unsafe entry", func(t *testing.T) {
		a := newTestApp(t)
		archive := makeModPackage(t, map[string]string{"Addon/../../escape": "bad"})
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, modUploadRequest(t, a, archive, "addon.zip"))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("POST upload=%d %s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(filepath.Join(a.paths.Root, "escape")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe entry escaped: %v", err)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		a := newTestApp(t)
		writeFile(t, filepath.Join(a.paths.Mods, "Addon", "payload.txt"), "original")
		archive := makeModPackage(t, map[string]string{"Addon/ModInfo.xml": `<xml><ModInfo><ID value="addon"/></ModInfo>`})
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, modUploadRequest(t, a, archive, "addon.zip"))
		if rr.Code != http.StatusConflict || string(mustRead(t, filepath.Join(a.paths.Mods, "Addon", "payload.txt"))) != "original" {
			t.Fatalf("POST upload=%d %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("active", func(t *testing.T) {
		a := newTestApp(t)
		a.server.mu.Lock()
		a.server.state = ServerRunning
		a.server.mu.Unlock()
		archive := makeModPackage(t, map[string]string{"Addon/ModInfo.xml": `<xml><ModInfo><ID value="addon"/></ModInfo>`})
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, modUploadRequest(t, a, archive, "addon.zip"))
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"mods_locked"`) {
			t.Fatalf("POST upload=%d %s", rr.Code, rr.Body.String())
		}
	})
}

func modUploadRequest(t *testing.T, a *App, archive, name string) *http.Request {
	t.Helper()
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("package", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/mods/upload", body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	return req
}

func makeModPackage(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for name, content := range files {
		var entry io.Writer
		if strings.HasSuffix(name, "/") {
			header := &zip.FileHeader{Name: name}
			header.SetMode(os.ModeDir | 0700)
			entry, err = zw.CreateHeader(header)
		} else {
			entry, err = zw.Create(name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModsHTTPLockedAndRequiredErrors(t *testing.T) {
	a := newTestApp(t)
	writeModInfo(t, filepath.Join(a.paths.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	writeModInfo(t, filepath.Join(a.paths.Mods, "Addon"), `<xml><ModInfo><ID value="addon"/><Name value="Addon Display"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/><Dependencies><Dependency id="core"/></Dependencies></ModInfo></xml>`)
	a.server.mu.Lock()
	a.server.state = ServerStarting
	a.server.mu.Unlock()
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPut, "/api/mods/Core", strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"mods_locked"`) {
		t.Fatalf("locked=%d %s", rr.Code, rr.Body.String())
	}
	a.server.mu.Lock()
	a.server.state = ServerStopped
	a.server.mu.Unlock()
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPut, "/api/mods/Core", strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `Addon Display`) || strings.Contains(rr.Body.String(), a.paths.Mods) {
		t.Fatalf("required=%d %s", rr.Code, rr.Body.String())
	}
}

func TestConsoleHTTPQueryAuthAndCommandValidation(t *testing.T) {
	a := newTestApp(t)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/js/console.js", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "mergeEntries") {
		t.Fatalf("console asset=%d %s", rr.Code, rr.Body.String())
	}
	a.console.Append("game", "info", "log", "line")
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/console?channel=game&limit=1", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"text":"line"`) {
		t.Fatalf("GET=%d %s", rr.Code, rr.Body.String())
	}
	for _, query := range []string{"channel=unknown", "channel=game&after=nope", "channel=game&limit=0"} {
		rr = httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/console?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("query %q = %d", query, rr.Code)
		}
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/console/telnet", strings.NewReader(`{"command":"lp"}`))
	req.RemoteAddr = "127.0.0.1:51000"
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated=%d", rr.Code)
	}
	for _, command := range []string{"say hi\nnow", "adminpassword hunter2"} {
		rr = httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/console/telnet", strings.NewReader(`{"command":`+strconv.Quote(command)+`}`)))
		if rr.Code != http.StatusBadRequest || strings.Contains(rr.Body.String(), "hunter2") {
			t.Fatalf("command %q = %d %s", command, rr.Code, rr.Body.String())
		}
	}
}

func TestManualTelnetCommandDoesNotUpdateOnlinePlayers(t *testing.T) {
	a := newTestApp(t)
	a.server.mu.Lock()
	a.server.state = ServerRunning
	a.server.launched = &fakeLaunchedProcess{pid: 42}
	a.server.mu.Unlock()
	a.server.SetOnlinePlayers(3)
	client, peer := net.Pipe()
	defer peer.Close()
	a.telnet = func(string) (TelnetClient, error) {
		return TelnetClient{CommandIdle: time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}, nil
	}
	go func() {
		_, _ = peer.Write([]byte("Welcome> \r\n"))
		command := make([]byte, len("lp\r\n"))
		_, _ = io.ReadFull(peer, command)
		_, _ = peer.Write([]byte("Total of 7 in the game\r\n> "))
	}()

	if _, err := a.runTelnetCommand(context.Background(), "lp"); err != nil {
		t.Fatal(err)
	}
	if got := a.server.Status().OnlinePlayers; got == nil || *got != 3 {
		t.Fatalf("online players=%v, want retained poll value 3", got)
	}
}

func TestRejectsNonLoopbackClient(t *testing.T) {
	a := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/status", nil)
	req.RemoteAddr = "192.168.1.50:51000"
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestRejectsUnconfiguredHostBeforeAnyRoute(t *testing.T) {
	a := newTestApp(t)
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/session"},
		{http.MethodPost, "/api/setup/prepare"},
	} {
		req := httptest.NewRequest(test.method, "http://evil.test:8787"+test.path, nil)
		req.RemoteAddr = "127.0.0.1:51000"
		req.Header.Set("Origin", "http://evil.test:8787")
		req.Header.Set("X-Panel-Token", a.token)
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s got %d", test.method, test.path, rr.Code)
		}
	}
}

func TestAllowsConfiguredLoopbackHostAliases(t *testing.T) {
	a := newTestApp(t)
	for _, host := range []string{"127.0.0.1:8787", "localhost:8787"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/session", nil)
		req.RemoteAddr = "127.0.0.1:51000"
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("host %s got %d: %s", host, rr.Code, rr.Body)
		}
	}
}

func TestAllowsDefaultHTTPPortAuthorities(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:80")
	for _, test := range []struct {
		method string
		url    string
		origin string
	}{
		{http.MethodGet, "http://localhost/api/session", ""},
		{http.MethodGet, "http://localhost:80/api/session", ""},
		{http.MethodGet, "http://LOCALHOST/api/session", ""},
		{http.MethodPost, "http://localhost/api/setup/prepare", "http://localhost"},
	} {
		req := httptest.NewRequest(test.method, test.url, nil)
		req.RemoteAddr = "127.0.0.1:51000"
		req.Header.Set("X-Panel-Token", a.token)
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %s origin=%q got %d: %s", test.method, test.url, test.origin, rr.Code, rr.Body)
		}
	}
}

func TestRejectsInvalidHostAndOriginAuthorities(t *testing.T) {
	for _, test := range []struct {
		name   string
		listen string
		method string
		url    string
		origin string
	}{
		{"wrong port", "127.0.0.1:80", http.MethodGet, "http://localhost:81/api/session", ""},
		{"evil host", "127.0.0.1:80", http.MethodGet, "http://evil.test/api/session", ""},
		{"https origin", "127.0.0.1:80", http.MethodPost, "http://localhost/api/setup/prepare", "https://localhost"},
		{"different origin host", "127.0.0.1:80", http.MethodPost, "http://localhost/api/setup/prepare", "http://127.0.0.1"},
		{"origin userinfo", "127.0.0.1:80", http.MethodPost, "http://localhost/api/setup/prepare", "http://user@localhost"},
		{"origin path", "127.0.0.1:80", http.MethodPost, "http://localhost/api/setup/prepare", "http://localhost/path"},
		{"empty origin host", "127.0.0.1:80", http.MethodPost, "http://localhost/api/setup/prepare", "http://"},
		{"missing nondefault port", "127.0.0.1:8787", http.MethodGet, "http://localhost/api/session", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAppAt(t, test.listen)
			req := httptest.NewRequest(test.method, test.url, nil)
			req.RemoteAddr = "127.0.0.1:51000"
			req.Header.Set("X-Panel-Token", a.token)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("got %d: %s", rr.Code, rr.Body)
			}
		})
	}
}

func TestSetupPrepareReturnsInspection(t *testing.T) {
	a := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/setup/prepare", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	if !a.paths.Inspect().UserData {
		t.Fatal("prepare did not create userdata")
	}
}

func TestBackupRoutesListAndCreate(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(a.paths.UserData, 0700); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "http://127.0.0.1:8787/api/backups", nil)
		req.RemoteAddr = "127.0.0.1:51000"
		if method == http.MethodPost {
			req.Header.Set("X-Panel-Token", a.token)
			req.Header.Set("Origin", "http://127.0.0.1:8787")
		}
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s backups: %d %s", method, rr.Code, rr.Body)
		}
	}
}

func TestBackupRoutePublishesStartAndCompleteEvents(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(a.paths.UserData, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.UserData+"\\save.dat", []byte("save"), 0600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/backups", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	_, cancel, events := a.events.subscribe()
	defer cancel()
	if len(events) < 2 || events[len(events)-2].Message != "开始创建备份" || events[len(events)-1].Message != "备份完成" {
		t.Fatalf("backup events = %#v", events)
	}
}

func TestConfigWritesRejectActiveMaintenanceLease(t *testing.T) {
	a := newTestApp(t)
	release, err := a.server.BeginStoppedOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, test := range []struct {
		method, path, body string
	}{
		{http.MethodPut, "/api/config", `{}`},
		{http.MethodPost, "/api/setup/userdata", `{}`},
		{http.MethodPut, "/api/sandbox/raw", `{"code":"A"}`},
		{http.MethodPut, "/api/sandbox/options", `{"edits":{}}`},
	} {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "http://127.0.0.1:8787"+test.path, strings.NewReader(test.body))
			req.RemoteAddr = "127.0.0.1:51000"
			req.Header.Set("X-Panel-Token", a.token)
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "server_busy") {
				t.Fatalf("got %d: %s", rr.Code, rr.Body)
			}
		})
	}
}

func TestSandboxRouteUsesRawOnlyForMismatchedSchema(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /></ServerSettings>`)
	if err := SaveSandboxCache(a.paths.SandboxCache, "V3.1.0-b14", json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"id":0,"values":["off","on"]}]}`)); err != nil {
		t.Fatal(err)
	}
	a.server.mu.Lock()
	a.server.build = "V3.2.0-b1"
	a.server.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sandbox", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET sandbox = %d: %s", rr.Code, rr.Body)
	}
	var response struct {
		Mode    string          `json:"mode"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Mode != "raw-only" || response.Payload != nil {
		t.Fatalf("response=%#v", response)
	}
}

func TestSandboxRouteUsesRawOnlyForUnusableSchema(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /></ServerSettings>`)
	cache, err := json.Marshal(SandboxCache{Build: "V3.1.0-b14", Payload: json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, a.paths.SandboxCache, string(cache))
	a.server.mu.Lock()
	a.server.build = "V3.1.0-b14"
	a.server.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sandbox", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	var response struct {
		Mode    string          `json:"mode"`
		Payload json.RawMessage `json:"payload"`
	}
	if rr.Code != http.StatusOK || json.NewDecoder(rr.Body).Decode(&response) != nil || response.Mode != "raw-only" || response.Payload != nil {
		t.Fatalf("status=%d response=%#v", rr.Code, response)
	}
}

func TestSandboxRouteRequiresExactBuild(t *testing.T) {
	for _, build := range []string{"v3.1.0-b14", " V3.1.0-b14", "V3.1.0-b14 "} {
		t.Run(build, func(t *testing.T) {
			a := newTestApp(t)
			writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /></ServerSettings>`)
			if err := SaveSandboxCache(a.paths.SandboxCache, "V3.1.0-b14", json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"id":0,"values":["off","on"]}]}`)); err != nil {
				t.Fatal(err)
			}
			a.server.mu.Lock()
			a.server.build = build
			a.server.mu.Unlock()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sandbox", nil)
			req.RemoteAddr = "127.0.0.1:51000"
			a.Handler().ServeHTTP(rr, req)
			var response struct {
				Mode string `json:"mode"`
			}
			if rr.Code != http.StatusOK || json.NewDecoder(rr.Body).Decode(&response) != nil || response.Mode != "raw-only" {
				t.Fatalf("status=%d response=%#v", rr.Code, response)
			}
		})
	}
}

func TestSandboxRouteReportsDashboardStateWithoutSecrets(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /><property name="WebDashboardEnabled" value="true" /><property name="WebDashboardPort" value="8080" /><property name="TelnetPassword" value="secret-sentinel" /></ServerSettings>`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sandbox", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	a.Handler().ServeHTTP(rr, req)
	var response struct {
		Dashboard struct {
			Enabled bool `json:"enabled"`
			Port    int  `json:"port"`
		} `json:"dashboard"`
	}
	if rr.Code != http.StatusOK || json.NewDecoder(rr.Body).Decode(&response) != nil {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !response.Dashboard.Enabled || response.Dashboard.Port != 8080 {
		t.Fatalf("dashboard=%#v", response.Dashboard)
	}
	if strings.Contains(rr.Body.String(), "secret-sentinel") || strings.Contains(rr.Body.String(), "TelnetPassword") {
		t.Fatalf("response leaked secret: %s", rr.Body.String())
	}
}

func TestSandboxRefreshUsesCurrentConfiguredDashboardPort(t *testing.T) {
	calls := 0
	dashboard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("X-SDTD-API-TOKENNAME"); got != "panel" {
			t.Fatalf("token name=%q", got)
		}
		if got := r.Header.Get("X-SDTD-API-SECRET"); got != "secret" {
			t.Fatalf("token secret=%q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("unexpected Authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/openapi/openapi.yaml":
			_, _ = w.Write([]byte("paths:\n  /SandboxSettings:\n    get: {}\n"))
		case "/SandboxSettings":
			_, _ = w.Write([]byte(`{"build":"V3.1.0-b14","code":"A","options":[{"id":0,"values":["off","on"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer dashboard.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(dashboard.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /><property name="WebDashboardEnabled" value="true" /><property name="WebDashboardPort" value="`+port+`" /></ServerSettings>`)
	a.server.mu.Lock()
	a.server.state, a.server.launched, a.server.build = ServerRunning, &fakeLaunchedProcess{pid: 1}, "V3.1.0-b14"
	a.server.mu.Unlock()
	a.dashboard.HTTP = dashboard.Client()
	var audit []string
	a.audit = func(message string) error { audit = append(audit, message); return nil }
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/sandbox/refresh", strings.NewReader(`{"tokenName":"panel","tokenSecret":"secret"}`)))
	if rr.Code != http.StatusOK || calls != 2 {
		t.Fatalf("status=%d calls=%d body=%s", rr.Code, calls, rr.Body.String())
	}
	if text := strings.Join(audit, "\n"); strings.Contains(text, "panel") || strings.Contains(text, "secret") {
		t.Fatalf("audit leaked credential: %s", text)
	}
}

func TestSandboxRefreshRejectsDisabledOrInvalidDashboardConfiguration(t *testing.T) {
	for _, test := range []struct{ name, config, want string }{
		{"disabled", `<ServerSettings><property name="SandboxCode" value="A" /><property name="WebDashboardEnabled" value="false" /><property name="WebDashboardPort" value="8080" /></ServerSettings>`, "dashboard_disabled"},
		{"missing", `<ServerSettings><property name="SandboxCode" value="A" /><property name="WebDashboardEnabled" value="true" /></ServerSettings>`, "dashboard_unavailable"},
		{"invalid", `<ServerSettings><property name="SandboxCode" value="A" /><property name="WebDashboardEnabled" value="true" /><property name="WebDashboardPort" value="invalid" /></ServerSettings>`, "dashboard_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newTestApp(t)
			writeFile(t, a.paths.ServerConfig, test.config)
			a.server.mu.Lock()
			a.server.state, a.server.launched = ServerRunning, &fakeLaunchedProcess{pid: 1}
			a.server.mu.Unlock()
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/sandbox/refresh", nil))
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), test.want) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSandboxRouteEnrichesOfficialOptionsWithoutChangingPayload(t *testing.T) {
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="SandboxCode" value="A" /></ServerSettings>`)
	writeFile(t, a.paths.LocalizationCSV, "Key,english,schinese,tchinese\ngoBlockDamage,Block Damage,方块伤害,方塊傷害\n")
	payload := json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"id":2,"name":"BlockDamage","values":["1","2"]}]}`)
	if err := SaveSandboxCache(a.paths.SandboxCache, "V3.1.0-b14", payload); err != nil {
		t.Fatal(err)
	}
	a.server.mu.Lock()
	a.server.build = "V3.1.0-b14"
	a.server.mu.Unlock()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/sandbox", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	a.Handler().ServeHTTP(rr, req)
	var response struct {
		Payload     json.RawMessage          `json:"payload"`
		OptionTexts map[string]LocalizedText `json:"optionTexts"`
	}
	if rr.Code != http.StatusOK || json.NewDecoder(rr.Body).Decode(&response) != nil {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := response.OptionTexts["2"].Labels; got["zh-CN"] != "方块伤害" || got["en"] != "Block Damage" {
		t.Fatalf("labels=%#v", got)
	}
	if strings.TrimSpace(string(response.Payload)) != strings.TrimSpace(string(payload)) {
		t.Fatalf("payload was mutated: %s", response.Payload)
	}
}

func TestConfigRouteEnrichesWithFallbackWhenLocalizationIsUnavailable(t *testing.T) {
	for name, writeLocalization := range map[string]bool{"missing": false, "malformed": true} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="ServerPort" value="26900" /><!-- Port used by the game server. --></ServerSettings>`)
			if writeLocalization {
				writeFile(t, a.paths.LocalizationCSV, "not a localization file")
			}
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/api/config", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("GET config = %d: %s", rr.Code, rr.Body.String())
			}
			var doc ConfigDocument
			if err := json.NewDecoder(rr.Body).Decode(&doc); err != nil {
				t.Fatal(err)
			}
			if doc.Properties[0].Text.Labels["en"] != "Server Port" || doc.Properties[0].EnglishComment != "Port used by the game server." {
				t.Fatalf("property=%#v", doc.Properties[0])
			}
		})
	}
}

func TestStoppedServerRejectsCredentialedAPIsBeforeExternalAccess(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.ServerConfig, []byte(`<ServerSettings><property name="TelnetPassword" value="secret-sentinel" /></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	telnetCalls := 0
	a.telnet = func(string) (TelnetClient, error) {
		telnetCalls++
		return TelnetClient{}, errors.New("must not read credentials")
	}
	httpCalls := 0
	dashboard := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { httpCalls++ }))
	defer dashboard.Close()
	a.dashboard = DashboardClient{BaseURL: dashboard.URL, HTTP: dashboard.Client()}

	for _, test := range []struct{ path, body string }{
		{"/api/server/stop", ``},
		{"/api/server/console", `{"command":"help"}`},
		{"/api/sandbox/refresh", `{"tokenName":"panel","tokenSecret":"secret-sentinel"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+test.path, strings.NewReader(test.body))
		req.RemoteAddr = "127.0.0.1:51000"
		req.Header.Set("X-Panel-Token", a.token)
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "server_stopped") {
			t.Fatalf("%s got %d: %s", test.path, rr.Code, rr.Body)
		}
	}
	if telnetCalls != 0 || httpCalls != 0 {
		t.Fatalf("external access: telnet=%d http=%d", telnetCalls, httpCalls)
	}
	if b, err := os.ReadFile(filepath.Join(a.paths.Logs, "panel.log")); err == nil && strings.Contains(string(b), "secret-sentinel") {
		t.Fatalf("audit leaked credential: %s", b)
	}
}

func TestStopServerAcceptsShutdownAfterCommandWrite(t *testing.T) {
	a := newTestApp(t)
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: a.paths.ServerExe, Started: started}}
	a.server = newServerManager(a.paths, a.events, probe, nil)
	writeRecordedProcess(t, a.paths, persistedProcess{PID: 42, Exe: a.paths.ServerExe, Started: started})
	a.server.shutdownTimeout = 100 * time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	a.telnet = func(string) (TelnetClient, error) {
		return TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}, nil
	}
	go func() {
		_, _ = peer.Write([]byte("Started Telnet session.\r\n"))
		buf := make([]byte, len("shutdown\r\n"))
		_, _ = io.ReadFull(peer, buf)
		probe.setErr(os.ErrProcessDone)
		_ = peer.Close()
	}()
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/server/stop", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST stop = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestConfigAuditAttemptFailurePreventsSave(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.ServerConfig, []byte(`<ServerSettings><property name="ServerName" value="old" /></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	a.audit = NewAuditLog(a.paths.Root).Write
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8787/api/config", strings.NewReader(`{"hash":"`+doc.Hash+`","updates":{"ServerName":"new"}}`))
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "audit_failed") {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	got, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := configValue(got, "ServerName")
	if value != "old" {
		t.Fatalf("config changed despite audit failure: %q", value)
	}
}

func TestConfigResultAuditFailureReportsAlreadyExecuted(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.ServerConfig, []byte(`<ServerSettings><property name="ServerName" value="old" /></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.audit = func(string) error {
		calls++
		if calls == 2 {
			return errors.New("result audit unavailable")
		}
		return nil
	}
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8787/api/config", strings.NewReader(`{"hash":"`+doc.Hash+`","updates":{"ServerName":"new"}}`))
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "操作已执行但审计失败") {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	got, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := configValue(got, "ServerName")
	if value != "new" {
		t.Fatalf("config did not execute before result audit failure: %q", value)
	}
}

func updateRequest(a *App, ctx context.Context, target UpdateTarget) *http.Request {
	path := "/api/update/server"
	if target == UpdateSteamCMD {
		path = "/api/update/steamcmd"
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+path, nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	return req
}

type fakeFirewallBackend struct {
	rules   []FirewallRule
	applied []FirewallChange
	err     error
	noApply bool
}

func (b *fakeFirewallBackend) List(context.Context) ([]FirewallRule, error) {
	return append([]FirewallRule(nil), b.rules...), nil
}

func (b *fakeFirewallBackend) Apply(_ context.Context, changes []FirewallChange) error {
	b.applied = append([]FirewallChange(nil), changes...)
	if b.err != nil || b.noApply {
		return b.err
	}
	rules := map[string]FirewallRule{}
	for _, rule := range b.rules {
		rules[rule.ID] = rule
	}
	for _, change := range changes {
		switch change.Action {
		case "add", "modify":
			rules[change.Rule.ID] = change.Rule
		case "remove":
			delete(rules, change.Rule.ID)
		}
	}
	b.rules = b.rules[:0]
	for _, rule := range rules {
		b.rules = append(b.rules, rule)
	}
	return nil
}

func firewallTestApp(t *testing.T, backend *fakeFirewallBackend, admin bool) *App {
	t.Helper()
	a := newTestApp(t)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="ServerPort" value="26900"/></ServerSettings>`)
	a.firewall = backend
	a.firewallAdmin = func() bool { return admin }
	return a
}

func firewallPreviewRequest(t *testing.T, a *App) string {
	t.Helper()
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/preview", strings.NewReader(`{}`)))
	var response struct {
		Hash string `json:"hash"`
	}
	if rr.Code != http.StatusOK || json.NewDecoder(rr.Body).Decode(&response) != nil || response.Hash == "" {
		t.Fatalf("preview=%d %s", rr.Code, rr.Body.String())
	}
	return response.Hash
}

func TestFirewallHTTPAdminGateAndClientChanges(t *testing.T) {
	backend := &fakeFirewallBackend{}
	a := firewallTestApp(t, backend, false)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/firewall", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"canApply":false`) {
		t.Fatalf("GET=%d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"nope"}`)))
	if rr.Code != http.StatusForbidden || len(backend.applied) != 0 {
		t.Fatalf("non-admin apply=%d applied=%#v", rr.Code, backend.applied)
	}
	a.firewallAdmin = func() bool { return true }
	hash := firewallPreviewRequest(t, a)
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"`+hash+`","changes":[{"action":"remove"}]}`)))
	if rr.Code != http.StatusBadRequest || len(backend.applied) != 0 {
		t.Fatalf("client changes=%d %s applied=%#v", rr.Code, rr.Body.String(), backend.applied)
	}
}

func TestFirewallHTTPRejectsPanelListenerPort(t *testing.T) {
	a := firewallTestApp(t, &fakeFirewallBackend{}, true)
	rr := httptest.NewRecorder()
	body := `{"settings":{"custom":[{"name":"panel","protocol":"TCP","ports":"8787","profile":"Any","note":"blocked"}]}}`
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPut, "/api/firewall/settings", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_firewall_settings") {
		t.Fatalf("panel port=%d %s", rr.Code, rr.Body.String())
	}
}

func TestFirewallHTTPRecomputesOwnedChangesAndVerifiesResult(t *testing.T) {
	thirdParty := FirewallRule{ID: "third-party", Name: "third-party", Protocol: "TCP", Ports: "80", Profile: "Any", Enabled: true}
	backend := &fakeFirewallBackend{rules: []FirewallRule{thirdParty}}
	a := firewallTestApp(t, backend, true)
	hash := firewallPreviewRequest(t, a)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"`+hash+`"}`)))
	if rr.Code != http.StatusOK || len(backend.applied) != 2 {
		t.Fatalf("apply=%d %s changes=%#v", rr.Code, rr.Body.String(), backend.applied)
	}
	for _, change := range backend.applied {
		if !firewallOwned(change.Rule) {
			t.Fatalf("third-party reached Apply: %#v", change)
		}
	}
	mismatch := &fakeFirewallBackend{noApply: true}
	a = firewallTestApp(t, mismatch, true)
	hash = firewallPreviewRequest(t, a)
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"`+hash+`"}`)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "firewall_verify_failed") {
		t.Fatalf("verify=%d %s", rr.Code, rr.Body.String())
	}
}

func TestFirewallHTTPRejectsStalePreviewAndSanitizesOps(t *testing.T) {
	a := firewallTestApp(t, &fakeFirewallBackend{}, true)
	hash := firewallPreviewRequest(t, a)
	writeFile(t, a.paths.ServerConfig, `<ServerSettings><property name="ServerPort" value="26901"/></ServerSettings>`)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"`+hash+`"}`)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "firewall_stale") {
		t.Fatalf("stale=%d %s", rr.Code, rr.Body.String())
	}
	failing := &fakeFirewallBackend{err: errors.New(`C:\secret-sentinel\failure`)}
	a = firewallTestApp(t, failing, true)
	hash = firewallPreviewRequest(t, a)
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodPost, "/api/firewall/apply", strings.NewReader(`{"hash":"`+hash+`"}`)))
	entries := a.console.Since("ops", 0, 10)
	if rr.Code != http.StatusBadRequest || len(entries) == 0 || strings.Contains(entries[len(entries)-1].Text, "secret-sentinel") {
		t.Fatalf("failure=%d %s entries=%#v", rr.Code, rr.Body.String(), entries)
	}
}

func TestUpdateRequestReturnsAcceptedAndOutlivesRequestContext(t *testing.T) {
	for _, target := range []UpdateTarget{UpdateServer, UpdateSteamCMD} {
		t.Run(string(target), func(t *testing.T) {
			a := newTestApp(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan error, 1)
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			u, _ := asyncTestUpdater(t, func(ctx context.Context, _ string, _ []string, _ func(string)) error {
				close(entered)
				select {
				case <-release:
					finished <- nil
					return nil
				case <-ctx.Done():
					finished <- ctx.Err()
					return ctx.Err()
				}
			})
			a.updater = u
			requestContext, cancelRequest := context.WithCancel(context.Background())
			rr := httptest.NewRecorder()
			returned := make(chan struct{})
			go func() {
				a.Handler().ServeHTTP(rr, updateRequest(a, requestContext, target))
				close(returned)
			}()
			<-entered
			cancelRequest()
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("update handler waited for the background update")
			}
			if rr.Code != http.StatusAccepted {
				t.Fatalf("POST update = %d: %s", rr.Code, rr.Body)
			}

			other := UpdateServer
			if target == UpdateServer {
				other = UpdateSteamCMD
			}
			second := httptest.NewRecorder()
			a.Handler().ServeHTTP(second, updateRequest(a, context.Background(), other))
			if second.Code != http.StatusConflict {
				t.Fatalf("second POST update = %d: %s", second.Code, second.Body)
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-finished; err != nil {
				t.Fatalf("background update inherited request cancellation: %v", err)
			}
			waitUpdateState(t, u, "success")
		})
	}
}

func TestUpdateRoutesRejectInvalidTargetAndOldRoute(t *testing.T) {
	a := newTestApp(t)
	invalid := httptest.NewRecorder()
	a.runUpdate(invalid, UpdateTarget("invalid"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid target = %d: %s", invalid.Code, invalid.Body)
	}
	old := httptest.NewRecorder()
	a.Handler().ServeHTTP(old, saveRequest(a, http.MethodPost, "/api/update/run", nil))
	if old.Code != http.StatusNotFound {
		t.Fatalf("old update route = %d: %s", old.Code, old.Body)
	}
}

func TestServeCancellationCancelsBackgroundUpdate(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	entered := make(chan struct{})
	canceled := make(chan error, 1)
	allowCleanup := make(chan struct{})
	cleaned := make(chan struct{})
	var cleanupOnce sync.Once
	t.Cleanup(func() { cleanupOnce.Do(func() { close(allowCleanup) }) })
	u, _ := asyncTestUpdater(t, func(ctx context.Context, _ string, _ []string, _ func(string)) error {
		close(entered)
		<-ctx.Done()
		canceled <- ctx.Err()
		<-allowCleanup
		close(cleaned)
		return ctx.Err()
	})
	a.updater = u
	if err := u.Start(a.lifetime, UpdateServer); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- a.Serve(ctx) }()
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve cancellation did not cancel updater")
	}
	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned before updater cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cleanupOnce.Do(func() { close(allowCleanup) })
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("Serve returned before updater cleanup completed")
	}
	if status := waitUpdateState(t, u, "canceled"); status.LastError == "" {
		t.Fatalf("canceled status = %#v", status)
	}
	if err := u.Start(a.lifetime, UpdateServer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start after App lifetime cancellation = %v", err)
	}
	lease, err := u.server.BeginStoppedOperation()
	if err != nil {
		t.Fatalf("Serve returned before updater lease release: %v", err)
	}
	lease()
}

func TestServeCancellationClosesPerformanceMonitor(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	closed := make(chan struct{})
	a.performanceClose = func() error {
		if !errors.Is(a.lifetime.Err(), context.Canceled) {
			return errors.New("performance monitor closed before panel lifetime cancellation")
		}
		close(closed)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- a.Serve(ctx) }()
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Serve cancellation did not close performance monitor")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestServeCancellationClosesEventStreamImmediately(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	a := newTestAppAt(t, address)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- a.Serve(ctx) }()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	var response *http.Response
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get("http://" + address + "/api/events")
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("open event stream: %v", err)
	}
	t.Cleanup(func() { response.Body.Close() })
	started := time.Now()
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("Serve took %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not close EventSource within one second")
	}
}

func TestServeReportsUpdaterCleanupFailure(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	entered := make(chan struct{})
	want := errors.New("updater cleanup failed")
	u, _ := asyncTestUpdater(t, func(ctx context.Context, _ string, _ []string, _ func(string)) error {
		close(entered)
		<-ctx.Done()
		return want
	})
	a.updater = u
	if err := u.Start(a.lifetime, UpdateServer); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- a.Serve(ctx) }()
	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, want) {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not join failed updater")
	}
}

func TestIndexReferencedAssetsAreServed(t *testing.T) {
	a := newTestApp(t)
	page, err := web.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`(?:src|href)="/([^"#?]+)"`).FindAllStringSubmatch(string(page), -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no local assets")
	}
	for _, ref := range refs {
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/"+ref[1], nil))
		if rr.Code != http.StatusOK || strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET /%s = %d %q", ref[1], rr.Code, rr.Header().Get("Content-Type"))
		}
	}
}

func TestStaticRejectsTestsAndUnknownFiles(t *testing.T) {
	a := newTestApp(t)
	for _, target := range []string{"/app.test.js", "/missing.js", "/.hidden.js", "/web/app.js"} {
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, target, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d", target, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, saveRequest(a, http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("GET / = %d csp=%q", rr.Code, rr.Header().Get("Content-Security-Policy"))
	}
}
