package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (a *App) steamSettings() SteamSettings {
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil {
		return SteamSettings{}
	}
	return config.Steam
}

func (a *App) versionReport() VersionReport {
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil {
		config = a.cfg
	}
	return a.versions.Report(readInstalledGame(a.paths.ServerDir), config.Steam, !config.NoUpdateCheck)
}

func (a *App) getVersions(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.versionReport())
}

func (a *App) checkVersions(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	switch body.Target {
	case "panel":
		a.versions.CheckPanel(req.Context())
	case "game":
		branches, err := a.updater.GameBranches(req.Context())
		if errors.Is(err, ErrServerMaintenance) || errors.Is(err, ErrServerBusy) {
			a.error(w, http.StatusConflict, "server_busy", "SteamCMD is busy")
			return
		}
		a.versions.recordGame(branches, err)
	default:
		a.error(w, http.StatusBadRequest, "invalid_request", "target must be panel or game")
		return
	}
	a.json(w, http.StatusOK, a.versionReport())
}

func (a *App) saveVersionSettings(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Branch       string `json:"branch"`
		Password     string `json:"password"`
		KeepPassword bool   `json:"keepPassword"`
		AutoCheck    bool   `json:"autoCheck"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	current := a.steamSettings()
	settings := SteamSettings{Branch: body.Branch, BranchPassword: body.Password}
	if body.KeepPassword && body.Password == "" && body.Branch == current.Branch {
		settings.BranchPassword = current.BranchPassword
	}
	if err := validateSteamSettings(settings); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_branch", sanitizedError(err))
		return
	}
	if !a.auditAttempt(w, "steam branch="+settings.Branch) {
		return
	}
	_, err := updatePanelConfig(a.paths.PanelConfig, func(config *PanelConfig) {
		config.Steam = settings
		config.NoUpdateCheck = !body.AutoCheck
	})
	if auditErr := a.auditResult("steam branch="+settings.Branch, err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "version_settings_failed", sanitizedError(err))
		return
	}
	a.json(w, http.StatusOK, a.versionReport())
}

// versionLoop checks for a new panel release daily and for a new game build
// every few hours, announcing each new version once.
func (a *App) versionLoop(ctx context.Context, first, interval time.Duration) {
	if !waitContext(ctx, first) {
		return
	}
	for {
		config, err := LoadPanelConfig(a.paths.PanelConfig)
		if err == nil && !config.NoUpdateCheck {
			a.checkVersionsInBackground(ctx)
		}
		if !waitContext(ctx, interval) {
			return
		}
	}
}

func (a *App) checkVersionsInBackground(ctx context.Context) {
	panelDue, gameDue := a.versions.due()
	if panelDue {
		if result := a.versions.CheckPanel(ctx); result.UpdateAvailable && a.versions.announce("panel "+result.Latest) {
			a.events.Publish(Event{Type: "update", Source: "version", Message: "面板有新版本 " + result.Latest, Data: result})
		}
	}
	if gameDue && exists(a.paths.SteamCMD) && readInstalledGame(a.paths.ServerDir).Installed {
		branches, err := a.updater.GameBranches(ctx)
		if errors.Is(err, ErrServerMaintenance) || errors.Is(err, ErrServerBusy) {
			return // try again next time
		}
		a.versions.recordGame(branches, err)
		if game := a.versionReport().Game; game.UpdateAvailable && a.versions.announce("game "+game.Branch+" "+game.Latest) {
			a.events.Publish(Event{Type: "update", Source: "version", Message: "游戏有新版本 " + game.Branch + " build " + game.Latest, Data: game})
		}
	}
}
