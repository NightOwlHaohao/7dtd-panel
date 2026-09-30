package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scheduled work: restarts with in-game countdowns, backups, commands and
// announcements, plus restarting the game after a crash. Times are the
// panel computer's local time.

const (
	TaskRestart = "restart"
	TaskBackup  = "backup"
	TaskCommand = "command"
	TaskSay     = "say"
)

const (
	maxScheduledTasks   = 50
	maxTaskTimes        = 24
	maxTaskEveryMinutes = 7 * 24 * 60
	defaultBackupKeep   = 10
	maxBackupKeep       = 500
	maxRestartWarnings  = 5
	maxWarningMinutes   = 60
)

var ErrInvalidSchedule = errors.New("invalid schedule")

type ScheduledTask struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Action   string   `json:"action"`
	Command  string   `json:"command,omitempty"`  // command: console command; say: message; restart: warning text with {minutes}
	Every    int      `json:"every,omitempty"`    // minutes between runs; 0 = at Times
	Times    []string `json:"times,omitempty"`    // "HH:MM"
	Days     []int    `json:"days,omitempty"`     // 0 = Sunday … 6; empty = every day
	Warnings []int    `json:"warnings,omitempty"` // restart: announce this many minutes before
	Keep     int      `json:"keep,omitempty"`     // backup: scheduled backups to keep
}

type ScheduleSettings struct {
	// AutoStartServer starts the game when the panel starts, for example
	// after Windows boots with the service set to start automatically.
	AutoStartServer bool `json:"autoStartServer"`
	// CrashRestart starts the game again after it exits unexpectedly.
	CrashRestart bool            `json:"crashRestart"`
	Tasks        []ScheduledTask `json:"tasks"`
}

// normalizeSchedule validates settings and fills in IDs and defaults.
func normalizeSchedule(settings ScheduleSettings) (ScheduleSettings, error) {
	if len(settings.Tasks) > maxScheduledTasks {
		return settings, fmt.Errorf("%w: at most %d tasks", ErrInvalidSchedule, maxScheduledTasks)
	}
	ids := map[string]bool{}
	tasks := make([]ScheduledTask, 0, len(settings.Tasks))
	for i, task := range settings.Tasks {
		task, err := normalizeTask(task)
		if err != nil {
			return settings, fmt.Errorf("%w: task %d: %v", ErrInvalidSchedule, i+1, err)
		}
		if ids[task.ID] {
			task.ID = newTaskID()
		}
		ids[task.ID] = true
		tasks = append(tasks, task)
	}
	settings.Tasks = tasks
	return settings, nil
}

func normalizeTask(task ScheduledTask) (ScheduledTask, error) {
	task.Name = strings.TrimSpace(task.Name)
	task.Command = strings.TrimSpace(task.Command)
	if task.ID == "" || len(task.ID) > 32 || strings.ContainsFunc(task.ID, func(r rune) bool { return !isIDRune(r) }) {
		task.ID = newTaskID()
	}
	if task.Name == "" || len([]rune(task.Name)) > 100 {
		return task, errors.New("name must be 1-100 characters")
	}
	switch task.Action {
	case TaskBackup:
		task.Command = ""
	case TaskRestart:
		if task.Command != "" && !validConsoleCommand(task.Command) {
			return task, errors.New("the warning text must be one printable line of at most 500 characters")
		}
	case TaskCommand, TaskSay:
		if !validConsoleCommand(task.Command) {
			return task, errors.New("command must be one printable line of at most 500 characters")
		}
	default:
		return task, fmt.Errorf("unknown action %q", task.Action)
	}
	if task.Every < 0 || task.Every > maxTaskEveryMinutes {
		return task, fmt.Errorf("every must be 1-%d minutes", maxTaskEveryMinutes)
	}
	if (task.Every > 0) == (len(task.Times) > 0) {
		return task, errors.New("set either an interval or times of day")
	}
	if len(task.Times) > maxTaskTimes {
		return task, fmt.Errorf("at most %d times of day", maxTaskTimes)
	}
	times := make([]string, 0, len(task.Times))
	for _, value := range task.Times {
		hour, minute, ok := parseClock(value)
		if !ok {
			return task, fmt.Errorf("time %q is not HH:MM", value)
		}
		times = append(times, fmt.Sprintf("%02d:%02d", hour, minute))
	}
	sort.Strings(times)
	task.Times = slices.Compact(times)
	days := make([]int, 0, len(task.Days))
	for _, day := range task.Days {
		if day < 0 || day > 6 {
			return task, fmt.Errorf("day %d is not 0-6", day)
		}
		days = append(days, day)
	}
	sort.Ints(days)
	task.Days = slices.Compact(days)
	if len(task.Days) == 7 {
		task.Days = nil
	}
	if task.Action == TaskRestart {
		warnings := make([]int, 0, len(task.Warnings))
		for _, minutes := range task.Warnings {
			if minutes < 1 || minutes > maxWarningMinutes {
				return task, fmt.Errorf("warnings must be 1-%d minutes", maxWarningMinutes)
			}
			warnings = append(warnings, minutes)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(warnings)))
		task.Warnings = slices.Compact(warnings)
		if len(task.Warnings) > maxRestartWarnings {
			return task, fmt.Errorf("at most %d warnings", maxRestartWarnings)
		}
		if task.Every > 0 && len(task.Warnings) > 0 && task.Warnings[0] >= task.Every {
			return task, errors.New("the first warning must come after the previous restart")
		}
	} else {
		task.Warnings = nil
	}
	if task.Action == TaskBackup {
		if task.Keep == 0 {
			task.Keep = defaultBackupKeep
		}
		if task.Keep < 1 || task.Keep > maxBackupKeep {
			return task, fmt.Errorf("keep must be 1-%d", maxBackupKeep)
		}
	} else {
		task.Keep = 0
	}
	return task, nil
}

func isIDRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
}

func newTaskID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func parseClock(value string) (int, int, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, false
	}
	return parsed.Hour(), parsed.Minute(), true
}

// lead is how long before its time a task starts: restarts begin with
// their first warning.
func (task ScheduledTask) lead() time.Duration {
	if task.Action == TaskRestart && len(task.Warnings) > 0 {
		return time.Duration(task.Warnings[0]) * time.Minute
	}
	return 0
}

// nextAfter returns the first scheduled time strictly after after. Interval
// tasks count from anchor, the previous run or when the schedule started.
func (task ScheduledTask) nextAfter(after, anchor time.Time) time.Time {
	if task.Every > 0 {
		step := time.Duration(task.Every) * time.Minute
		next := anchor.Add(step)
		if !next.After(after) {
			missed := after.Sub(next)/step + 1
			next = next.Add(missed * step)
		}
		return next
	}
	location := after.Location()
	year, month, day := after.Date()
	for offset := 0; offset <= 7; offset++ {
		date := time.Date(year, month, day+offset, 0, 0, 0, 0, location)
		if len(task.Days) > 0 && !slices.Contains(task.Days, int(date.Weekday())) {
			continue
		}
		for _, value := range task.Times {
			hour, minute, _ := parseClock(value)
			candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, location)
			if candidate.After(after) {
				return candidate
			}
		}
	}
	return time.Time{}
}

// TaskStatus is the runtime state of one task, shown next to its settings.
type TaskStatus struct {
	ID         string     `json:"id"`
	NextRun    *time.Time `json:"nextRun,omitempty"`
	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastResult string     `json:"lastResult,omitempty"` // success, skipped, error
	LastError  string     `json:"lastError,omitempty"`
	Running    bool       `json:"running"`
}

type taskState struct {
	next, lastRun     time.Time
	anchor            time.Time
	result, lastError string
	running           bool
	signature         string
	cancel            context.CancelFunc
}

// errTaskSkipped marks a run that had nothing to do, such as a restart
// while the game is stopped.
var errTaskSkipped = errors.New("skipped")

// Scheduler runs ScheduledTasks. run performs one task; target is the time
// the task is scheduled for (a restart waits for it after its warnings).
type Scheduler struct {
	mu       sync.Mutex
	settings ScheduleSettings
	state    map[string]*taskState
	started  time.Time
	now      func() time.Time
	run      func(ctx context.Context, task ScheduledTask, target time.Time) error
	report   func(task ScheduledTask, result string, err error)
	wg       sync.WaitGroup
}

func NewScheduler(settings ScheduleSettings, run func(context.Context, ScheduledTask, time.Time) error) *Scheduler {
	s := &Scheduler{now: time.Now, run: run, state: map[string]*taskState{}}
	s.started = s.now()
	s.settings = settings
	s.refreshLocked(s.started)
	return s
}

func taskSignature(task ScheduledTask) string {
	return fmt.Sprintf("%s|%d|%v|%v|%v", task.Action, task.Every, task.Times, task.Days, task.Warnings)
}

// refreshLocked recomputes next runs after the settings changed, keeping
// the history of tasks whose timing did not change.
func (s *Scheduler) refreshLocked(now time.Time) {
	seen := map[string]bool{}
	for _, task := range s.settings.Tasks {
		seen[task.ID] = true
		state := s.state[task.ID]
		if state == nil {
			state = &taskState{anchor: now}
			s.state[task.ID] = state
		}
		signature := taskSignature(task)
		if state.signature != signature {
			state.signature = signature
			if state.lastRun.IsZero() {
				state.anchor = now
			}
			state.next = time.Time{}
		}
		if !task.Enabled {
			state.next = time.Time{}
			continue
		}
		if state.next.IsZero() {
			anchor := state.anchor
			if !state.lastRun.IsZero() {
				anchor = state.lastRun
			}
			// A restart must not start its countdown in the past.
			state.next = task.nextAfter(now.Add(task.lead()), anchor)
		}
	}
	for id, state := range s.state {
		if !seen[id] {
			if state.cancel != nil {
				state.cancel()
			}
			delete(s.state, id)
		}
	}
}

func (s *Scheduler) Settings() ScheduleSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Scheduler) Update(settings ScheduleSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
	s.refreshLocked(s.now())
}

func (s *Scheduler) Status() []TaskStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskStatus, 0, len(s.settings.Tasks))
	for _, task := range s.settings.Tasks {
		state := s.state[task.ID]
		status := TaskStatus{ID: task.ID}
		if state != nil {
			status.Running, status.LastResult, status.LastError = state.running, state.result, state.lastError
			if !state.next.IsZero() {
				next := state.next
				status.NextRun = &next
			}
			if !state.lastRun.IsZero() {
				last := state.lastRun
				status.LastRun = &last
			}
		}
		out = append(out, status)
	}
	return out
}

// Tick starts every task whose start time (its time minus its lead) has
// come. It is called by Loop and directly by tests.
func (s *Scheduler) Tick(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	var due []ScheduledTask
	var targets []time.Time
	for _, task := range s.settings.Tasks {
		state := s.state[task.ID]
		if !task.Enabled || state == nil || state.running || state.next.IsZero() {
			continue
		}
		if now.Before(state.next.Add(-task.lead())) {
			continue
		}
		target := state.next
		if now.Sub(target) > 5*time.Minute {
			// Missed (the computer slept or the panel was busy): don't run
			// stale work, just move on to the next time.
			state.next = task.nextAfter(now.Add(task.lead()), target)
			continue
		}
		due = append(due, task)
		targets = append(targets, target)
	}
	s.mu.Unlock()
	for i, task := range due {
		s.start(ctx, task, targets[i])
	}
}

// RunNow starts a task immediately, outside its schedule.
func (s *Scheduler) RunNow(ctx context.Context, id string) error {
	s.mu.Lock()
	var found *ScheduledTask
	for _, task := range s.settings.Tasks {
		if task.ID == id {
			task := task
			found = &task
		}
	}
	s.mu.Unlock()
	if found == nil {
		return fmt.Errorf("%w: unknown task", ErrInvalidSchedule)
	}
	task := *found
	task.Warnings = nil // run now means now
	if !s.start(ctx, task, s.now()) {
		return ErrServerBusy
	}
	return nil
}

func (s *Scheduler) start(ctx context.Context, task ScheduledTask, target time.Time) bool {
	s.mu.Lock()
	state := s.state[task.ID]
	if state == nil || state.running {
		s.mu.Unlock()
		return false
	}
	runCtx, cancel := context.WithCancel(ctx)
	state.running, state.cancel = true, cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer cancel()
		err := s.run(runCtx, task, target)
		result := "success"
		if errors.Is(err, errTaskSkipped) {
			result = "skipped"
		} else if err != nil {
			result = "error"
		}
		s.mu.Lock()
		if state := s.state[task.ID]; state != nil {
			now := s.now()
			state.running, state.cancel = false, nil
			state.lastRun, state.result, state.lastError = now, result, ""
			if result != "success" {
				state.lastError = sanitizedError(err)
			}
			if current, ok := s.taskLocked(task.ID); ok && current.Enabled {
				anchor := target
				if current.Every > 0 && anchor.IsZero() {
					anchor = now
				}
				state.next = current.nextAfter(now.Add(current.lead()), anchor)
			}
		}
		report := s.report
		s.mu.Unlock()
		if report != nil {
			report(task, result, err)
		}
	}()
	return true
}

func (s *Scheduler) taskLocked(id string) (ScheduledTask, bool) {
	for _, task := range s.settings.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return ScheduledTask{}, false
}

// Loop ticks until ctx ends, then waits for running tasks to return.
func (s *Scheduler) Loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-ticker.C:
		}
	}
}

// Wait blocks until running tasks have returned.
func (s *Scheduler) Wait() { s.wg.Wait() }

// ---------- Crash restart ----------

const (
	crashRestartLimit  = 3
	crashRestartWindow = 30 * time.Minute
)

// crashWatcher polls the server state (which is what notices a crash when
// no browser is open) and starts the game again when enabled. After
// crashRestartLimit restarts within crashRestartWindow it gives up, so a
// broken mod or save doesn't loop forever.
type crashWatcher struct {
	status   func() ServerStatus
	enabled  func() bool
	start    func() error
	notify   func(message string)
	delay    time.Duration
	now      func() time.Time
	seen     time.Time
	restarts []time.Time
}

func (w *crashWatcher) check(ctx context.Context) {
	status := w.status()
	if status.LastCrash == nil || !status.LastCrash.After(w.seen) {
		return
	}
	w.seen = *status.LastCrash
	if !w.enabled() {
		return
	}
	now := w.now()
	recent := w.restarts[:0]
	for _, at := range w.restarts {
		if now.Sub(at) < crashRestartWindow {
			recent = append(recent, at)
		}
	}
	w.restarts = recent
	if len(w.restarts) >= crashRestartLimit {
		w.notify(fmt.Sprintf("游戏在 %d 分钟内崩溃了 %d 次，已停止自动重启", int(crashRestartWindow.Minutes()), len(w.restarts)+1))
		return
	}
	if !waitContext(ctx, w.delay) {
		return
	}
	if state := w.status().State; state != ServerStopped {
		return // started by hand meanwhile
	}
	w.restarts = append(w.restarts, now)
	if err := w.start(); err != nil {
		w.notify("崩溃后自动重启失败: " + sanitizedError(err))
		return
	}
	w.notify("游戏崩溃，已自动重启")
}

func (w *crashWatcher) loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
