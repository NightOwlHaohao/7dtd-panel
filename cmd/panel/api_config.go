package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
)

type configUpdateRequest struct {
	Hash    string            `json:"hash"`
	Updates map[string]string `json:"updates"`
}

type hashRequest struct {
	Hash string `json:"hash"`
}

type sandboxRawRequest struct {
	Hash  string `json:"hash"`
	Code  string `json:"code"`
	Force bool   `json:"force"`
}

type sandboxOptionsRequest struct {
	Hash  string      `json:"hash"`
	Edits map[int]int `json:"edits"`
}

type sandboxRefreshRequest struct {
	TokenName   string `json:"tokenName"`
	TokenSecret string `json:"tokenSecret"`
}

func (a *App) config(w http.ResponseWriter, _ *http.Request) {
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", err.Error())
		return
	}
	catalog, _ := LoadLocalization(a.paths.LocalizationCSV)
	a.json(w, http.StatusOK, EnrichConfig(doc, catalog))
}

func (a *App) saveConfig(w http.ResponseWriter, req *http.Request) {
	var body configUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return
	}
	release, ok := a.beginConfigMutation(w, "config save")
	if !ok {
		return
	}
	defer release()
	hash, err := SaveConfig(a.paths.ServerConfig, a.paths.ConfigBackups, body.Hash, body.Updates)
	if auditErr := a.auditResult("config save", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.configSaveError(w, err)
		return
	}
	a.json(w, http.StatusOK, map[string]string{"hash": hash})
}

func (a *App) setupUserData(w http.ResponseWriter, req *http.Request) {
	var body hashRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return
	}
	release, ok := a.beginConfigMutation(w, "setup userdata")
	if !ok {
		return
	}
	defer release()
	hash, err := EnsureUserDataFolder(a.paths, body.Hash)
	if auditErr := a.auditResult("setup userdata", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.configSaveError(w, err)
		return
	}
	a.json(w, http.StatusOK, map[string]string{"hash": hash})
}

func (a *App) configSaveError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrServerBusy) || errors.Is(err, ErrServerMaintenance) {
		a.error(w, http.StatusConflict, "server_busy", err.Error())
		return
	}
	if errors.Is(err, ErrConfigChanged) {
		a.error(w, http.StatusConflict, "config_changed", err.Error())
		return
	}
	if errors.Is(err, ErrPropertyNotFound) {
		a.error(w, http.StatusBadRequest, "property_not_found", err.Error())
		return
	}
	a.error(w, http.StatusBadRequest, "config_save_failed", err.Error())
}

func (a *App) sandbox(w http.ResponseWriter, _ *http.Request) {
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", err.Error())
		return
	}
	raw, ok := configValue(doc, "SandboxCode")
	if !ok {
		a.error(w, http.StatusBadRequest, "property_not_found", "SandboxCode not found")
		return
	}
	code, parseErr := ParseSandboxCode(raw)
	enabled, port := dashboardState(doc)
	response := map[string]any{"hash": doc.Hash, "code": raw, "mode": "raw-only", "dashboard": map[string]any{"enabled": enabled, "port": port}}
	if parseErr == nil {
		response["records"] = code.Records
	} else {
		response["warning"] = parseErr.Error()
	}
	if cache, err := readSandboxCache(a.paths.SandboxCache); err == nil {
		serverBuild := a.server.Status().Build
		if knownSandboxBuild(serverBuild) && cache.Build == serverBuild {
			response["mode"], response["build"], response["cachedAt"], response["payload"] = "cache", cache.Build, cache.FetchedAt, cache.Payload
			catalog, _ := LoadLocalization(a.paths.LocalizationCSV)
			response["optionTexts"] = sandboxOptionTexts(cache.Payload, catalog)
		}
	}
	a.json(w, http.StatusOK, response)
}

func (a *App) saveSandboxRaw(w http.ResponseWriter, req *http.Request) {
	var body sandboxRawRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	release, ok := a.beginConfigMutation(w, "sandbox raw save")
	if !ok {
		return
	}
	defer release()
	if _, err := ParseSandboxCode(body.Code); err != nil && !body.Force {
		a.json(w, http.StatusConflict, map[string]any{"code": "sandbox_warning", "message": err.Error(), "canForce": true})
		return
	}
	hash, err := SaveConfig(a.paths.ServerConfig, a.paths.ConfigBackups, body.Hash, map[string]string{"SandboxCode": body.Code})
	if auditErr := a.auditResult("sandbox raw save", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.configSaveError(w, err)
		return
	}
	a.json(w, http.StatusOK, map[string]string{"hash": hash})
}

func (a *App) saveSandboxOptions(w http.ResponseWriter, req *http.Request) {
	var body sandboxOptionsRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	release, ok := a.beginConfigMutation(w, "sandbox options save")
	if !ok {
		return
	}
	defer release()
	cache, err := readSandboxCache(a.paths.SandboxCache)
	serverBuild := a.server.Status().Build
	if err != nil || !knownSandboxBuild(serverBuild) || cache.Build != serverBuild {
		a.error(w, http.StatusConflict, "sandbox_build_mismatch", "Sandbox definitions are not for the current build")
		return
	}
	if err := ValidateSandboxEdits(cache.Payload, body.Edits); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_sandbox_edit", err.Error())
		return
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", err.Error())
		return
	}
	raw, ok := configValue(doc, "SandboxCode")
	if !ok {
		a.error(w, http.StatusBadRequest, "property_not_found", "SandboxCode not found")
		return
	}
	code, err := ParseSandboxCode(raw)
	if err != nil {
		a.json(w, http.StatusConflict, map[string]any{"code": "sandbox_warning", "message": err.Error(), "canForce": true})
		return
	}
	hash, err := SaveConfig(a.paths.ServerConfig, a.paths.ConfigBackups, body.Hash, map[string]string{"SandboxCode": MergeSandbox(code, body.Edits).String()})
	if auditErr := a.auditResult("sandbox options save", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.configSaveError(w, err)
		return
	}
	a.json(w, http.StatusOK, map[string]string{"hash": hash})
}

func (a *App) refreshSandbox(w http.ResponseWriter, req *http.Request) {
	var body sandboxRefreshRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && err != io.EOF {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := a.server.RequireRunning(); err != nil {
		a.error(w, http.StatusConflict, "server_stopped", err.Error())
		return
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", err.Error())
		return
	}
	raw, ok := configValue(doc, "SandboxCode")
	if !ok {
		a.error(w, http.StatusBadRequest, "property_not_found", "SandboxCode not found")
		return
	}
	enabled, port := dashboardState(doc)
	if !enabled {
		a.error(w, http.StatusBadRequest, "dashboard_disabled", "Web Dashboard is disabled")
		return
	}
	if port == 0 {
		a.error(w, http.StatusBadRequest, "dashboard_unavailable", "Web Dashboard port is not configured")
		return
	}
	dashboard := a.dashboard
	dashboard.BaseURL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dashboard.TokenName = body.TokenName
	dashboard.TokenSecret = body.TokenSecret
	payload, err := dashboard.FetchSandbox(req.Context(), raw, true)
	if errors.Is(err, ErrDashboardAuthRequired) {
		a.error(w, http.StatusUnauthorized, "dashboard_auth_required", err.Error())
		return
	}
	if err != nil {
		a.error(w, http.StatusBadGateway, "sandbox_refresh_failed", err.Error())
		return
	}
	build := a.server.Status().Build
	if !knownSandboxBuild(build) {
		a.error(w, http.StatusConflict, "sandbox_build_unknown", "Server build is not available yet")
		return
	}
	payload, err = withSandboxPayloadBuild(payload, build)
	if err != nil {
		a.error(w, http.StatusBadGateway, "sandbox_refresh_failed", err.Error())
		return
	}
	if err := SaveSandboxCache(a.paths.SandboxCache, build, payload); err != nil {
		a.error(w, http.StatusBadRequest, "sandbox_cache_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, map[string]any{"build": build, "payload": payload})
}

func configValue(doc ConfigDocument, name string) (string, bool) {
	for _, p := range doc.Properties {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

func configString(doc ConfigDocument, name string) string {
	value, _ := configValue(doc, name)
	return value
}

func dashboardState(doc ConfigDocument) (bool, int) {
	enabled, err := strconv.ParseBool(configString(doc, "WebDashboardEnabled"))
	if err != nil || !enabled {
		return false, 0
	}
	port, err := strconv.Atoi(configString(doc, "WebDashboardPort"))
	if err != nil || port < 1 || port > 65535 {
		return true, 0
	}
	return true, port
}

func readSandboxCache(path string) (SandboxCache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return SandboxCache{}, err
	}
	var cache SandboxCache
	if err := json.Unmarshal(b, &cache); err != nil {
		return SandboxCache{}, err
	}
	if err := validateSandboxPayload(cache.Payload); err != nil {
		return SandboxCache{}, err
	}
	if !knownSandboxBuild(cache.Build) || sandboxPayloadBuild(cache.Payload) != cache.Build {
		return SandboxCache{}, ErrSandboxCacheBuildMismatch
	}
	return cache, nil
}
