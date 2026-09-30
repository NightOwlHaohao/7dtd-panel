package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webui "sevenpanel/web"
)

// web is the embedded browser UI (index.html, favicon.svg, css/, js/).
var web = webui.Files

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type App struct {
	paths             Paths
	cfg               PanelConfig
	mux               *http.ServeMux
	token             string
	events            *EventBroker
	console           *ConsoleStore
	firewall          FirewallBackend
	firewallAdmin     func() bool
	firewallMu        sync.Mutex
	firewallHash      string
	firewallUntil     time.Time
	server            *ServerManager
	mods              ModService
	saves             SaveService
	backups           BackupService
	updater           *Updater
	performance       *PerformanceMonitor
	performanceSample func(context.Context) (PerformanceSample, error)
	performanceClose  func() error
	audit             func(string) error
	telnet            func(string) (TelnetClient, error)
	dashboard         DashboardClient
	lifetime          context.Context
	cancelLifetime    context.CancelFunc
	service           serviceControl
	lifecycle         panelLifecycle
	scheduler         *Scheduler
	crash             *crashWatcher
	versions          *VersionChecker
	players           *PlayerHistory
	dialLocal         func(address string) bool
}

func NewApp(paths Paths, cfg PanelConfig) (*App, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return nil, err
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	audit := NewAuditLog(filepath.Join(paths.Logs, "panel.log"))
	a := &App{paths: paths, cfg: cfg, mux: http.NewServeMux(), token: hex.EncodeToString(bytes), events: NewEventBroker(), lifetime: lifetime, cancelLifetime: cancelLifetime, audit: audit.Write, telnet: telnetFromConfig, dashboard: DashboardClient{}, firewall: WindowsFirewallBackend{}, firewallAdmin: firewallCanApply}
	a.service = newServiceControl(paths, currentExecutable(), false)
	a.server = NewServerManager(paths, a.events)
	a.players = NewPlayerHistory(filepath.Join(paths.Cache, "players.json"))
	a.server.pollObserver = a.recordPlayers
	a.dialLocal = dialLocal
	a.performance = NewPerformanceMonitor(a.server)
	a.performanceSample = a.performance.Sample
	a.performanceClose = a.performance.Close
	a.console = NewConsoleStore()
	a.events.SetConsoleStore(a.console)
	a.mods = ModService{paths: paths, server: a.server}
	a.saves = SaveService{paths: paths, server: a.server}
	a.backups = BackupService{paths: paths, server: a.server}
	a.updater = NewUpdater(paths, a.server, a.events)
	a.updater.audit = a.audit
	a.updater.steam = a.steamSettings
	a.versions = NewVersionChecker(version)
	a.newScheduler(cfg.Schedule)
	a.mux.HandleFunc("GET /api/session", a.session)
	a.mux.HandleFunc("GET /api/setup", a.setup)
	a.mux.HandleFunc("POST /api/setup/prepare", a.prepare)
	a.mux.HandleFunc("POST /api/setup/userdata", a.setupUserData)
	a.mux.HandleFunc("GET /api/config", a.config)
	a.mux.HandleFunc("PUT /api/config", a.saveConfig)
	a.mux.HandleFunc("GET /api/sandbox", a.sandbox)
	a.mux.HandleFunc("PUT /api/sandbox/raw", a.saveSandboxRaw)
	a.mux.HandleFunc("PUT /api/sandbox/options", a.saveSandboxOptions)
	a.mux.HandleFunc("POST /api/sandbox/refresh", a.refreshSandbox)
	a.mux.HandleFunc("GET /api/events", a.eventStream)
	a.mux.HandleFunc("GET /api/console", a.listConsole)
	a.mux.HandleFunc("POST /api/console/telnet", a.consoleTelnet)
	a.mux.HandleFunc("GET /api/firewall", a.getFirewall)
	a.mux.HandleFunc("PUT /api/firewall/settings", a.saveFirewallSettings)
	a.mux.HandleFunc("POST /api/firewall/preview", a.previewFirewall)
	a.mux.HandleFunc("POST /api/firewall/apply", a.applyFirewall)
	a.mux.HandleFunc("GET /api/status", a.status)
	a.mux.HandleFunc("GET /api/performance", a.performanceStatus)
	a.mux.HandleFunc("POST /api/server/start", a.startServer)
	a.mux.HandleFunc("POST /api/server/stop", a.stopServer)
	a.mux.HandleFunc("POST /api/server/console", a.serverConsole)
	a.mux.HandleFunc("POST /api/server/force-stop", a.forceStopServer)
	a.mux.HandleFunc("GET /api/mods", a.listMods)
	a.mux.HandleFunc("POST /api/mods/upload", a.uploadMod)
	a.mux.HandleFunc("PUT /api/mods/policy", a.setModPolicy)
	a.mux.HandleFunc("PUT /api/mods/{name}", a.setModEnabled)
	a.mux.HandleFunc("GET /api/backups", a.listBackups)
	a.mux.HandleFunc("GET /api/saves", a.listSaves)
	a.mux.HandleFunc("POST /api/saves/switch", a.switchSave)
	a.mux.HandleFunc("POST /api/saves/export", a.exportSave)
	a.mux.HandleFunc("POST /api/saves/import", a.importSave)
	a.mux.HandleFunc("DELETE /api/saves/{world}/{game}", a.deleteSave)
	a.mux.HandleFunc("POST /api/backups", a.createBackup)
	a.mux.HandleFunc("POST /api/backups/open-folder", a.openBackupFolder)
	a.mux.HandleFunc("DELETE /api/backups/{name}", a.deleteBackup)
	a.mux.HandleFunc("POST /api/backups/{name}/restore", a.restoreBackup)
	a.mux.HandleFunc("GET /api/update", a.updateStatus)
	a.mux.HandleFunc("POST /api/update/server", a.runServerUpdate)
	a.mux.HandleFunc("POST /api/update/steamcmd", a.runSteamCMDUpdate)
	a.mux.HandleFunc("GET /api/schedule", a.getSchedule)
	a.mux.HandleFunc("PUT /api/schedule", a.saveSchedule)
	a.mux.HandleFunc("POST /api/schedule/tasks/{id}/run", a.runScheduleTask)
	a.mux.HandleFunc("GET /api/versions", a.getVersions)
	a.mux.HandleFunc("POST /api/versions/check", a.checkVersions)
	a.mux.HandleFunc("PUT /api/versions/settings", a.saveVersionSettings)
	a.mux.HandleFunc("GET /api/players", a.listPlayers)
	a.mux.HandleFunc("POST /api/players/action", a.playerAction)
	a.mux.HandleFunc("PUT /api/naiwazi/port", a.setNaiwaziPort)
	a.mux.HandleFunc("POST /api/naiwazi/password", a.naiwaziPassword)
	a.mux.HandleFunc("GET /api/panel", a.panelStatus)
	a.mux.HandleFunc("POST /api/panel/stop", a.stopPanel)
	a.mux.HandleFunc("POST /api/panel/service/install", a.installService)
	a.mux.HandleFunc("POST /api/panel/service/uninstall", a.uninstallService)
	a.mux.HandleFunc("PUT /api/panel/service/autostart", a.setServiceAutoStart)
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		a.error(w, http.StatusNotFound, "not_found", "接口不存在")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		a.mux.HandleFunc(method+" /api/", notFound)
	}
	a.mux.HandleFunc("GET /", a.static)
	return a, nil
}

func currentExecutable() string {
	exe, _ := os.Executable()
	return exe
}

func (a *App) Handler() http.Handler { return http.HandlerFunc(a.secure) }

func (a *App) Serve(ctx context.Context) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	a.lifecycle.bind(stop) // lets the UI stop the panel
	a.server.StartPoller()
	a.startBackground(a.lifetime)
	defer a.server.StopPoller()
	// No WriteTimeout: the event stream and a graceful stop (up to 2 minutes)
	// legitimately hold responses open.
	server := &http.Server{Addr: a.cfg.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	serveDone, shutdownDone := make(chan struct{}), make(chan error, 1)
	go func() {
		var shutdownErr error
		select {
		case <-ctx.Done():
			a.cancelLifetime()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr = server.Shutdown(shutdownCtx)
			cancel()
			if shutdownErr != nil {
				shutdownErr = errors.Join(shutdownErr, server.Close())
			}
		case <-serveDone:
			a.cancelLifetime()
		}
		performanceErr := a.performanceClose()
		updateErr := a.updater.Wait()
		if errors.Is(updateErr, context.Canceled) {
			updateErr = nil
		}
		shutdownDone <- errors.Join(shutdownErr, performanceErr, updateErr)
	}()
	listenErr := server.ListenAndServe()
	close(serveDone)
	shutdownErr := <-shutdownDone
	if listenErr == http.ErrServerClosed && ctx.Err() != nil {
		listenErr = nil
	}
	return errors.Join(listenErr, shutdownErr)
}

func (a *App) secure(w http.ResponseWriter, req *http.Request) {
	if !allowedHost(a.cfg.Listen, req.Host) {
		a.error(w, http.StatusForbidden, "forbidden", "请求 Host 未通过安全校验")
		return
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		a.error(w, http.StatusForbidden, "forbidden", "只允许本机访问")
		return
	}
	if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch || req.Method == http.MethodDelete {
		origin := req.Header.Get("Origin")
		_, defaultPort, _ := net.SplitHostPort(a.cfg.Listen)
		if req.Header.Get("X-Panel-Token") != a.token || (origin != "" && !sameHost(origin, req.Host, defaultPort)) {
			a.error(w, http.StatusForbidden, "forbidden", "请求未通过安全校验")
			return
		}
	}
	a.mux.ServeHTTP(w, req)
}

func allowedHost(listen, authority string) bool {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return false
	}
	_, configured := normalizePanelAuthority(listen, port)
	_, requested := normalizePanelAuthority(authority, port)
	return configured && requested
}

func normalizePanelAuthority(authority, defaultPort string) (string, bool) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		if defaultPort != "80" || strings.Contains(authority, ":") {
			return "", false
		}
		host, port = authority, defaultPort
	}
	host = strings.ToLower(host)
	if (host != "127.0.0.1" && host != "localhost") || port != defaultPort {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

func sameHost(origin, host, defaultPort string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	originAuthority, originOK := normalizePanelAuthority(u.Host, defaultPort)
	hostAuthority, hostOK := normalizePanelAuthority(host, defaultPort)
	return originOK && hostOK && originAuthority == hostAuthority
}

func (a *App) session(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, map[string]string{"token": a.token, "version": version})
}

func (a *App) setup(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.paths.Inspect())
}

func (a *App) prepare(w http.ResponseWriter, _ *http.Request) {
	if err := a.paths.PrepareDataDirs(); err != nil {
		a.error(w, http.StatusInternalServerError, "prepare_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, a.paths.Inspect())
}

func (a *App) eventStream(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(a.lifetime, cancel)
	defer stop()
	defer cancel()
	a.events.ServeHTTP(w, req.WithContext(ctx))
}

func (a *App) beginConfigMutation(w http.ResponseWriter, action string) (func(), bool) {
	if !a.auditAttempt(w, action) {
		return nil, false
	}
	release, err := a.server.BeginConfigMutation()
	if err != nil {
		if auditErr := a.auditResult(action, err); auditErr != nil {
			a.auditFailure(w, false, auditErr)
			return nil, false
		}
		a.configSaveError(w, err)
		return nil, false
	}
	return release, true
}

func (a *App) auditAttempt(w http.ResponseWriter, action string) bool {
	if err := a.writeAudit(action + " attempt"); err != nil {
		a.auditFailure(w, false, err)
		return false
	}
	return true
}

func (a *App) auditResult(action string, err error) error {
	result := action + " result=success"
	if err != nil {
		result = action + " result=failure reason=" + sanitizedError(err)
	}
	return a.writeAudit(result)
}

func (a *App) writeAudit(message string) error {
	if a.audit == nil {
		return nil
	}
	return a.audit(message)
}

func (a *App) auditFailure(w http.ResponseWriter, executed bool, err error) {
	message := "审计日志不可用，操作未执行: " + sanitizedError(err)
	if executed {
		message = "操作已执行但审计失败: " + sanitizedError(err)
	}
	a.error(w, http.StatusInternalServerError, "audit_failed", message)
}

var staticContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
}

// static serves the embedded web UI. Test files and dotfiles are never served.
func (a *App) static(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	contentType, known := staticContentTypes[path.Ext(name)]
	if !known || !fs.ValidPath(name) || strings.HasSuffix(name, ".test.js") || strings.HasPrefix(path.Base(name), ".") {
		a.error(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	data, err := web.ReadFile(name)
	if err != nil {
		a.error(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	if contentType == staticContentTypes[".html"] {
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		header.Set("X-Frame-Options", "DENY")
	}
	_, _ = w.Write(data)
}

func (a *App) json(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *App) error(w http.ResponseWriter, status int, code, message string) {
	a.json(w, status, APIError{code, message})
}
