package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultRestartWarning = "Server restart in {minutes} min / 服务器将在 {minutes} 分钟后重启"

// warningUnit is one minute; tests shorten it.
var warningUnit = time.Minute

type scheduleResponse struct {
	Settings ScheduleSettings `json:"settings"`
	Status   []TaskStatus     `json:"status"`
	Now      time.Time        `json:"now"`
}

func (a *App) newScheduler(settings ScheduleSettings) {
	normalized, err := normalizeSchedule(settings)
	if err != nil {
		_ = a.writeAudit("schedule settings ignored reason=" + sanitizedError(err))
		normalized = ScheduleSettings{}
	}
	a.scheduler = NewScheduler(normalized, a.runScheduledTask)
	a.scheduler.report = a.reportScheduledTask
	a.crash = &crashWatcher{
		status:  a.server.Status,
		enabled: func() bool { return a.scheduler.Settings().CrashRestart },
		start:   a.server.Start,
		notify:  func(message string) { a.scheduleEvent(message, nil) },
		delay:   10 * time.Second,
		now:     time.Now,
	}
}

// startBackground runs the scheduler, the crash watcher and, when enabled,
// starts the game. It stops with the panel.
func (a *App) startBackground(ctx context.Context) {
	go a.scheduler.Loop(ctx, 15*time.Second)
	go a.crash.loop(ctx, 5*time.Second)
	go a.versionLoop(ctx, 2*time.Minute, 30*time.Minute)
	if a.scheduler.Settings().AutoStartServer && exists(a.paths.ServerExe) && a.server.Status().State == ServerStopped {
		go func() {
			err := a.server.Start()
			_ = a.auditResult("server autostart", err)
			if err != nil {
				a.scheduleEvent("面板启动时自动启动游戏失败: "+sanitizedError(err), nil)
				return
			}
			a.scheduleEvent("面板启动时已自动启动游戏", nil)
		}()
	}
}

func (a *App) scheduleEvent(message string, data any) {
	if a.events != nil {
		a.events.Publish(Event{Type: "schedule", Source: "schedule", Message: message, Data: data})
	}
}

func (a *App) reportScheduledTask(task ScheduledTask, result string, err error) {
	message := fmt.Sprintf("计划任务“%s”：%s", task.Name, map[string]string{"success": "完成", "skipped": "已跳过", "error": "失败"}[result])
	if err != nil && result != "success" {
		message += "（" + sanitizedError(err) + "）"
	}
	_ = a.writeAudit("schedule task=" + task.ID + " action=" + task.Action + " result=" + result)
	a.scheduleEvent(message, map[string]string{"id": task.ID, "result": result})
}

func (a *App) runScheduledTask(ctx context.Context, task ScheduledTask, target time.Time) error {
	switch task.Action {
	case TaskRestart:
		return a.scheduledRestart(ctx, task, target)
	case TaskBackup:
		state := a.server.Status().State
		if state != ServerRunning && state != ServerStopped {
			return fmt.Errorf("%w: the server is %s", errTaskSkipped, state)
		}
		backup, err := a.backups.Create(ctx, true, a.saveWorld)
		if err != nil {
			return err
		}
		a.events.Publish(Event{Type: "backup", Source: "backup", Message: "自动备份完成", Data: backup})
		_, err = a.backups.PruneAuto(task.Keep)
		return err
	case TaskCommand:
		if a.server.Status().State != ServerRunning {
			return fmt.Errorf("%w: the server is not running", errTaskSkipped)
		}
		_, err := a.runTelnetCommand(ctx, task.Command)
		return err
	case TaskSay:
		if a.server.Status().State != ServerRunning {
			return fmt.Errorf("%w: the server is not running", errTaskSkipped)
		}
		return a.say(ctx, task.Command)
	}
	return fmt.Errorf("unknown action %q", task.Action)
}

// say broadcasts a chat message. Double quotes would end the argument, so
// they are replaced.
func (a *App) say(ctx context.Context, message string) error {
	_, err := a.runTelnetCommand(ctx, `say "`+strings.ReplaceAll(message, `"`, "'")+`"`)
	return err
}

// scheduledRestart announces the restart at each warning, then stops the
// game gracefully at target and starts it again.
func (a *App) scheduledRestart(ctx context.Context, task ScheduledTask, target time.Time) error {
	if a.server.Status().State != ServerRunning {
		return fmt.Errorf("%w: the server is not running", errTaskSkipped)
	}
	template := task.Command
	if template == "" {
		template = defaultRestartWarning
	}
	for _, minutes := range task.Warnings {
		at := target.Add(-time.Duration(minutes) * warningUnit)
		if time.Until(at) < -warningUnit/2 {
			continue // started late: skip warnings that are already past
		}
		if !waitContext(ctx, max(time.Until(at), 0)) {
			return ctx.Err()
		}
		if a.server.Status().State != ServerRunning {
			return fmt.Errorf("%w: the server stopped before the restart", errTaskSkipped)
		}
		// A failed announcement must not cancel the restart.
		_ = a.say(ctx, strings.ReplaceAll(template, "{minutes}", strconv.Itoa(minutes)))
	}
	if !waitContext(ctx, max(time.Until(target), 0)) {
		return ctx.Err()
	}
	if a.server.Status().State != ServerRunning {
		return fmt.Errorf("%w: the server stopped before the restart", errTaskSkipped)
	}
	client, err := a.telnet(a.paths.ServerConfig)
	if err != nil {
		return err
	}
	_ = a.writeAudit("schedule restart stop attempt task=" + task.ID)
	if err := a.server.Shutdown(ctx, client); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := a.server.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

func (a *App) getSchedule(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, scheduleResponse{Settings: a.scheduler.Settings(), Status: a.scheduler.Status(), Now: time.Now()})
}

func (a *App) saveSchedule(w http.ResponseWriter, req *http.Request) {
	var body ScheduleSettings
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	settings, err := normalizeSchedule(body)
	if err != nil {
		a.error(w, http.StatusBadRequest, "invalid_schedule", sanitizedError(err))
		return
	}
	if !a.auditAttempt(w, "schedule save") {
		return
	}
	_, err = updatePanelConfig(a.paths.PanelConfig, func(config *PanelConfig) { config.Schedule = settings })
	if auditErr := a.auditResult("schedule save", err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "schedule_save_failed", sanitizedError(err))
		return
	}
	a.scheduler.Update(settings)
	a.getSchedule(w, req)
}

func (a *App) runScheduleTask(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if !a.auditAttempt(w, "schedule run task="+id) {
		return
	}
	err := a.scheduler.RunNow(a.lifetime, id)
	_ = a.auditResult("schedule run task="+id, err)
	switch {
	case errors.Is(err, ErrInvalidSchedule):
		a.error(w, http.StatusNotFound, "schedule_task_not_found", "unknown task")
	case errors.Is(err, ErrServerBusy):
		a.error(w, http.StatusConflict, "schedule_task_running", "the task is already running")
	case err != nil:
		a.error(w, http.StatusBadRequest, "schedule_run_failed", sanitizedError(err))
	default:
		a.json(w, http.StatusAccepted, a.scheduler.Status())
	}
}
