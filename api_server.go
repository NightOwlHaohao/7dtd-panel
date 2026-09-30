package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

func (a *App) performanceStatus(w http.ResponseWriter, req *http.Request) {
	sample, err := a.performanceSample(req.Context())
	if err != nil {
		a.error(w, http.StatusServiceUnavailable, "performance_unavailable", "性能监视暂不可用")
		return
	}
	a.json(w, http.StatusOK, sample)
}

func (a *App) status(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.server.Status())
}

func (a *App) startServer(w http.ResponseWriter, _ *http.Request) {
	if !a.auditAttempt(w, "server start") {
		return
	}
	err := a.server.Start()
	if auditErr := a.auditResult("server start", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrServerRunning) {
			status = http.StatusConflict
		}
		a.error(w, status, "server_start_failed", err.Error())
		return
	}
	a.json(w, http.StatusAccepted, a.server.Status())
}

func (a *App) stopServer(w http.ResponseWriter, req *http.Request) {
	if err := a.server.RequireRunning(); err != nil {
		a.error(w, http.StatusConflict, "server_stopped", err.Error())
		return
	}
	client, err := a.telnet(a.paths.ServerConfig)
	if err == nil {
		err = a.server.Shutdown(req.Context(), client)
	}
	if err != nil {
		a.error(w, http.StatusBadRequest, "server_stop_failed", err.Error())
		return
	}
	a.json(w, http.StatusAccepted, a.server.Status())
}

func (a *App) serverConsole(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Command == "" {
		a.error(w, http.StatusBadRequest, "invalid_request", "command is required")
		return
	}
	if err := a.server.RequireRunning(); err != nil {
		a.error(w, http.StatusConflict, "server_stopped", err.Error())
		return
	}
	_, err := a.runTelnetCommand(req.Context(), body.Command)
	if err != nil {
		a.error(w, http.StatusBadRequest, "server_console_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, map[string]string{"output": "sent"})
}

type consoleResponse struct {
	Entries []ConsoleEntry `json:"entries"`
	Cursor  uint64         `json:"cursor"`
}

func (a *App) listConsole(w http.ResponseWriter, req *http.Request) {
	channel := req.URL.Query().Get("channel")
	if consoleChannel(channel) != channel {
		a.error(w, http.StatusBadRequest, "invalid_console_query", "invalid console query")
		return
	}
	after, err := strconv.ParseUint(req.URL.Query().Get("after"), 10, 64)
	if raw := req.URL.Query().Get("after"); raw != "" && err != nil {
		a.error(w, http.StatusBadRequest, "invalid_console_query", "invalid console query")
		return
	}
	limit := 200
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			a.error(w, http.StatusBadRequest, "invalid_console_query", "invalid console query")
			return
		}
	}
	entries := a.console.Since(channel, after, limit)
	if len(entries) > 0 {
		after = entries[len(entries)-1].Cursor
	}
	a.json(w, http.StatusOK, consoleResponse{Entries: entries, Cursor: after})
}

func (a *App) consoleTelnet(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Command string `json:"command"`
	}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	if !validConsoleCommand(body.Command) {
		a.error(w, http.StatusBadRequest, "invalid_console_command", "command is invalid")
		return
	}
	a.console.Append("telnet", "command", "command", body.Command)
	if _, err := a.runTelnetCommand(req.Context(), body.Command); err != nil {
		a.console.Append("telnet", "error", "error", err.Error())
		a.error(w, http.StatusBadRequest, "console_telnet_failed", "Telnet command failed")
		return
	}
	a.json(w, http.StatusOK, map[string]string{"output": "sent"})
}

func (a *App) runTelnetCommand(ctx context.Context, command string) (string, error) {
	if err := a.server.RequireRunning(); err != nil {
		return "", err
	}
	client, err := a.telnet(a.paths.ServerConfig)
	if err != nil {
		return "", err
	}
	output, err := client.Command(ctx, command)
	if err != nil {
		return "", err
	}
	if a.events != nil {
		a.events.Publish(Event{Type: "console", Source: "telnet", Message: output})
	}
	return output, nil
}

func (a *App) forceStopServer(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if !a.auditAttempt(w, "force-stop") {
		return
	}
	err := a.server.RequestForceStop(body.Confirm)
	if auditErr := a.auditResult("force-stop", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, ErrForceStopConfirmation) {
			status = http.StatusBadRequest
		}
		a.error(w, status, "server_force_stop_failed", err.Error())
		return
	}
	a.json(w, http.StatusAccepted, a.server.Status())
}

func (a *App) updateStatus(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.updater.Status())
}

func (a *App) runServerUpdate(w http.ResponseWriter, _ *http.Request) { a.runUpdate(w, UpdateServer) }

func (a *App) runSteamCMDUpdate(w http.ResponseWriter, _ *http.Request) {
	a.runUpdate(w, UpdateSteamCMD)
}

func (a *App) runUpdate(w http.ResponseWriter, target UpdateTarget) {
	if err := a.updater.Start(a.lifetime, target); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrAuditUnavailable) {
			status = http.StatusInternalServerError
		}
		if errors.Is(err, ErrServerRunning) || errors.Is(err, ErrServerMaintenance) || errors.Is(err, ErrServerBusy) {
			status = http.StatusConflict
		}
		a.error(w, status, "update_failed", err.Error())
		return
	}
	a.json(w, http.StatusAccepted, a.updater.Status())
}
