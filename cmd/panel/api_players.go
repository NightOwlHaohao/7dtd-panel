package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"
)

type playersResponse struct {
	Running     bool           `json:"running"`
	Online      []OnlinePlayer `json:"online"`
	OnlineError string         `json:"onlineError,omitempty"`
	Lists       AdminLists     `json:"lists"`
	Known       []KnownPlayer  `json:"known"`
	Naiwazi     NaiwaziStatus  `json:"naiwazi"`
}

func (a *App) recordPlayers(output string) {
	_ = a.players.Record(parseOnlinePlayers(output), time.Now())
}

func (a *App) naiwaziPort() int {
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil {
		return 0
	}
	return config.NaiwaziPort
}

func (a *App) listPlayers(w http.ResponseWriter, req *http.Request) {
	response := playersResponse{Online: []OnlinePlayer{}, Lists: readAdminLists(adminFilePath(a.paths)), Naiwazi: naiwaziStatus(a.paths, a.naiwaziPort(), a.dialLocal)}
	if a.server.Status().State == ServerRunning {
		response.Running = true
		output, err := a.runTelnetCommand(req.Context(), "lp")
		if err != nil {
			response.OnlineError = sanitizedError(err)
		} else {
			response.Online = parseOnlinePlayers(output)
			_ = a.players.Record(response.Online, time.Now())
		}
	}
	response.Known = a.players.List()
	a.json(w, http.StatusOK, response)
}

func (a *App) playerAction(w http.ResponseWriter, req *http.Request) {
	var body PlayerAction
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	command, err := playerCommand(body)
	if err != nil {
		a.error(w, http.StatusBadRequest, "invalid_player_action", sanitizedError(err))
		return
	}
	if err := a.server.RequireRunning(); err != nil {
		a.error(w, http.StatusConflict, "server_stopped", err.Error())
		return
	}
	action := "player " + body.Action + " target=" + body.Target
	if !a.auditAttempt(w, action) {
		return
	}
	a.console.Append("telnet", "command", "command", command)
	output, err := a.runTelnetCommand(req.Context(), command)
	if auditErr := a.auditResult(action, err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.error(w, http.StatusBadRequest, "player_action_failed", sanitizedError(err))
		return
	}
	a.json(w, http.StatusOK, map[string]string{"command": command, "output": output})
}

func (a *App) setNaiwaziPort(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Port int `json:"port"` // 0 = game port + 6
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Port < 0 || body.Port > 65535 {
		a.error(w, http.StatusBadRequest, "invalid_request", "port must be 0-65535")
		return
	}
	if _, err := updatePanelConfig(a.paths.PanelConfig, func(config *PanelConfig) { config.NaiwaziPort = body.Port }); err != nil {
		a.error(w, http.StatusInternalServerError, "naiwazi_settings_failed", sanitizedError(err))
		return
	}
	a.json(w, http.StatusOK, naiwaziStatus(a.paths, body.Port, a.dialLocal))
}

// naiwaziPassword is a POST so that it needs the session token.
func (a *App) naiwaziPassword(w http.ResponseWriter, _ *http.Request) {
	text, path, err := readNaiwaziPassword(a.paths)
	if errors.Is(err, os.ErrNotExist) {
		a.error(w, http.StatusNotFound, "naiwazi_password_missing", "NaiwaziBot has not written its password file yet")
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "naiwazi_password_failed", sanitizedError(err))
		return
	}
	_ = a.writeAudit("naiwazi password shown")
	a.json(w, http.StatusOK, map[string]string{"file": path, "text": text})
}
