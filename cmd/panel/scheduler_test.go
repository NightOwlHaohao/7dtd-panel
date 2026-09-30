package main

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestNormalizeScheduleValidatesAndFillsDefaults(t *testing.T) {
	settings, err := normalizeSchedule(ScheduleSettings{Tasks: []ScheduledTask{
		{Name: " Nightly ", Enabled: true, Action: TaskRestart, Times: []string{"4:05", "04:05", "23:00"}, Days: []int{6, 0, 1, 2, 3, 4, 5}, Warnings: []int{1, 10, 5, 5}},
		{Name: "Backup", Enabled: true, Action: TaskBackup, Every: 60, Command: "ignored"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	restart, backup := settings.Tasks[0], settings.Tasks[1]
	if restart.ID == "" || restart.Name != "Nightly" || strings.Join(restart.Times, ",") != "04:05,23:00" || restart.Days != nil {
		t.Fatalf("restart = %+v", restart)
	}
	if len(restart.Warnings) != 3 || restart.Warnings[0] != 10 || restart.Warnings[2] != 1 {
		t.Fatalf("warnings = %v, want 10,5,1", restart.Warnings)
	}
	if backup.Keep != defaultBackupKeep || backup.Command != "" || backup.Warnings != nil {
		t.Fatalf("backup = %+v", backup)
	}

	for name, task := range map[string]ScheduledTask{
		"no timing":        {Name: "x", Action: TaskSay, Command: "hi"},
		"both timings":     {Name: "x", Action: TaskSay, Command: "hi", Every: 5, Times: []string{"01:00"}},
		"bad time":         {Name: "x", Action: TaskSay, Command: "hi", Times: []string{"25:00"}},
		"bad day":          {Name: "x", Action: TaskSay, Command: "hi", Times: []string{"01:00"}, Days: []int{7}},
		"empty command":    {Name: "x", Action: TaskCommand, Every: 5},
		"multiline":        {Name: "x", Action: TaskCommand, Every: 5, Command: "say a\nshutdown"},
		"unknown action":   {Name: "x", Action: "format", Every: 5},
		"no name":          {Action: TaskBackup, Every: 5},
		"warning too long": {Name: "x", Action: TaskRestart, Every: 10, Warnings: []int{10}},
		"keep too large":   {Name: "x", Action: TaskBackup, Every: 10, Keep: 100000},
	} {
		if _, err := normalizeSchedule(ScheduleSettings{Tasks: []ScheduledTask{task}}); !errors.Is(err, ErrInvalidSchedule) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestNextAfterDailyTimesAndDays(t *testing.T) {
	task := ScheduledTask{Times: []string{"04:00", "16:00"}, Days: []int{int(time.Saturday)}}
	// 2026-09-30 is a Wednesday.
	got := task.nextAfter(mustTime(t, "2026-09-30 10:00"), time.Time{})
	if want := mustTime(t, "2026-10-03 04:00"); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	task.Days = nil
	if got := task.nextAfter(mustTime(t, "2026-09-30 10:00"), time.Time{}); !got.Equal(mustTime(t, "2026-09-30 16:00")) {
		t.Fatalf("next = %v", got)
	}
	if got := task.nextAfter(mustTime(t, "2026-09-30 16:00"), time.Time{}); !got.Equal(mustTime(t, "2026-10-01 04:00")) {
		t.Fatalf("a time equal to now is not next: %v", got)
	}
}

func TestNextAfterIntervalSkipsMissedRuns(t *testing.T) {
	task := ScheduledTask{Every: 30}
	anchor := mustTime(t, "2026-09-30 10:00")
	if got := task.nextAfter(mustTime(t, "2026-09-30 10:10"), anchor); !got.Equal(mustTime(t, "2026-09-30 10:30")) {
		t.Fatalf("next = %v", got)
	}
	if got := task.nextAfter(mustTime(t, "2026-09-30 12:05"), anchor); !got.Equal(mustTime(t, "2026-09-30 12:30")) {
		t.Fatalf("next after a long pause = %v", got)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func TestSchedulerRunsDueTasksOnceAndStartsRestartsEarly(t *testing.T) {
	clock := &fakeClock{now: mustTime(t, "2026-09-30 03:00")}
	var mu sync.Mutex
	var runs []string
	targets := map[string]time.Time{}
	s := &Scheduler{now: clock.Now, state: map[string]*taskState{}, run: func(_ context.Context, task ScheduledTask, target time.Time) error {
		mu.Lock()
		runs = append(runs, task.ID)
		targets[task.ID] = target
		mu.Unlock()
		return nil
	}}
	s.Update(ScheduleSettings{Tasks: []ScheduledTask{
		{ID: "restart", Name: "r", Enabled: true, Action: TaskRestart, Times: []string{"04:00"}, Warnings: []int{10, 1}},
		{ID: "say", Name: "s", Enabled: true, Action: TaskSay, Command: "hi", Every: 50},
		{ID: "off", Name: "o", Enabled: false, Action: TaskSay, Command: "hi", Every: 1},
	}})
	clock.Set(mustTime(t, "2026-09-30 03:49"))
	s.Tick(context.Background())
	s.Wait()
	if len(runs) != 0 {
		t.Fatalf("nothing is due yet: %v", runs)
	}
	clock.Set(mustTime(t, "2026-09-30 03:50"))
	s.Tick(context.Background())
	s.Wait()
	s.Tick(context.Background()) // already ran: must not run twice
	s.Wait()
	slices.Sort(runs)
	if strings.Join(runs, ",") != "restart,say" {
		t.Fatalf("runs = %v", runs)
	}
	if !targets["restart"].Equal(mustTime(t, "2026-09-30 04:00")) {
		t.Fatalf("restart target = %v; it starts 10 minutes early for the warnings", targets["restart"])
	}
	for _, status := range s.Status() {
		if status.ID == "off" && status.NextRun != nil {
			t.Fatalf("disabled task has a next run: %+v", status)
		}
		if status.ID == "restart" && (status.LastResult != "success" || !status.NextRun.Equal(mustTime(t, "2026-10-01 04:00"))) {
			t.Fatalf("restart status = %+v", status)
		}
	}
}

func TestSchedulerSkipsStaleRunsAndReportsSkips(t *testing.T) {
	clock := &fakeClock{now: mustTime(t, "2026-09-30 03:00")}
	calls := 0
	s := &Scheduler{now: clock.Now, state: map[string]*taskState{}, run: func(context.Context, ScheduledTask, time.Time) error {
		calls++
		return errTaskSkipped
	}}
	s.Update(ScheduleSettings{Tasks: []ScheduledTask{{ID: "a", Name: "a", Enabled: true, Action: TaskBackup, Times: []string{"04:00"}, Keep: 1}}})
	clock.Set(mustTime(t, "2026-09-30 09:00")) // the computer slept through 04:00
	s.Tick(context.Background())
	s.Wait()
	if calls != 0 {
		t.Fatal("a run missed by hours must not start late")
	}
	if status := s.Status()[0]; !status.NextRun.Equal(mustTime(t, "2026-10-01 04:00")) {
		t.Fatalf("status = %+v", status)
	}
	if err := s.RunNow(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if status := s.Status()[0]; calls != 1 || status.LastResult != "skipped" {
		t.Fatalf("calls=%d status=%+v", calls, status)
	}
	if err := s.RunNow(context.Background(), "missing"); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("unknown task error = %v", err)
	}
}

func TestCrashWatcherRestartsWithALimit(t *testing.T) {
	clock := &fakeClock{now: mustTime(t, "2026-09-30 10:00")}
	var crash time.Time
	starts := 0
	var messages []string
	w := &crashWatcher{
		status: func() ServerStatus {
			status := ServerStatus{State: ServerStopped}
			if !crash.IsZero() {
				c := crash
				status.LastCrash = &c
			}
			return status
		},
		enabled: func() bool { return true },
		start:   func() error { starts++; return nil },
		notify:  func(message string) { messages = append(messages, message) },
		now:     clock.Now,
	}
	w.check(context.Background())
	if starts != 0 {
		t.Fatal("no crash, no restart")
	}
	for i := 0; i < crashRestartLimit+1; i++ {
		crash = clock.Now().Add(time.Duration(i) * time.Minute)
		w.check(context.Background())
		w.check(context.Background()) // the same crash is handled once
	}
	if starts != crashRestartLimit {
		t.Fatalf("starts = %d, want %d then give up", starts, crashRestartLimit)
	}
	if last := messages[len(messages)-1]; !strings.Contains(last, "停止自动重启") {
		t.Fatalf("messages = %v", messages)
	}
	clock.Set(clock.Now().Add(crashRestartWindow + time.Minute))
	crash = clock.Now()
	w.check(context.Background())
	if starts != crashRestartLimit+1 {
		t.Fatal("restarts resume once old crashes leave the window")
	}
}

func TestCrashWatcherDoesNothingWhenDisabled(t *testing.T) {
	crash := time.Now()
	w := &crashWatcher{
		status:  func() ServerStatus { return ServerStatus{State: ServerStopped, LastCrash: &crash} },
		enabled: func() bool { return false },
		start:   func() error { t.Fatal("started while disabled"); return nil },
		notify:  func(string) {},
		now:     time.Now,
	}
	w.check(context.Background())
}

func TestScheduleRoutesSaveToPanelConfig(t *testing.T) {
	a := newTestApp(t)
	if _, err := LoadPanelConfig(a.paths.PanelConfig); err != nil { // creates panel.json
		t.Fatal(err)
	}
	body := `{"crashRestart":true,"tasks":[{"name":"Hourly save","enabled":true,"action":"command","command":"saveworld","every":60}]}`
	rr := serveJSON(t, a, "PUT", "/api/schedule", body)
	if rr.Code != 200 {
		t.Fatalf("PUT schedule = %d %s", rr.Code, rr.Body)
	}
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil || !config.Schedule.CrashRestart || len(config.Schedule.Tasks) != 1 || config.Schedule.Tasks[0].ID == "" {
		t.Fatalf("saved = %+v err=%v", config.Schedule, err)
	}
	if !a.scheduler.Settings().CrashRestart {
		t.Fatal("the running scheduler did not get the new settings")
	}
	if rr := serveJSON(t, a, "PUT", "/api/schedule", `{"tasks":[{"name":"x","action":"command","command":"a\nb","every":1}]}`); rr.Code != 400 || apiCode(t, rr) != "invalid_schedule" {
		t.Fatalf("invalid schedule = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/schedule/tasks/nope/run", ""); rr.Code != 404 {
		t.Fatalf("run unknown = %d", rr.Code)
	}
}

func TestScheduledSayQuotesTheMessage(t *testing.T) {
	a := newTestApp(t)
	a.server.mu.Lock()
	a.server.state = ServerRunning
	a.server.launched = &fakeLaunchedProcess{pid: 42}
	a.server.mu.Unlock()
	commands := scriptTelnet(t, a, func(string) string { return "" })
	if err := a.runScheduledTask(context.Background(), ScheduledTask{Action: TaskSay, Command: `Hi "all"`}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := commands(); len(got) != 1 || got[0] != `say "Hi 'all'"` {
		t.Fatalf("commands = %q", got)
	}
}

func TestScheduledTasksSkipWhileStopped(t *testing.T) {
	a := newTestApp(t)
	for _, task := range []ScheduledTask{{Action: TaskSay, Command: "x"}, {Action: TaskCommand, Command: "x"}, {Action: TaskRestart}} {
		if err := a.runScheduledTask(context.Background(), task, time.Now()); !errors.Is(err, errTaskSkipped) {
			t.Fatalf("%s: error = %v", task.Action, err)
		}
	}
}

// scriptTelnet answers each Telnet command with respond(command) and
// returns a function listing the commands received.
func scriptTelnet(t *testing.T, a *App, respond func(string) string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var commands []string
	a.telnet = func(string) (TelnetClient, error) {
		return TelnetClient{CommandIdle: 5 * time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) {
			client, peer := net.Pipe()
			go func() {
				defer peer.Close()
				_, _ = peer.Write([]byte("Logon successful.\r\n> "))
				line := make([]byte, 0, 256)
				one := make([]byte, 1)
				for {
					if _, err := peer.Read(one); err != nil {
						return
					}
					if one[0] == '\n' {
						break
					}
					line = append(line, one[0])
				}
				command := strings.TrimSuffix(string(line), "\r")
				mu.Lock()
				commands = append(commands, command)
				mu.Unlock()
				_, _ = peer.Write([]byte(respond(command) + "\r\n> "))
			}()
			return client, nil
		}}, nil
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), commands...)
	}
}

func TestScheduledRestartWarnsStopsAndStarts(t *testing.T) {
	previous := warningUnit
	warningUnit = 20 * time.Millisecond
	t.Cleanup(func() { warningUnit = previous })
	a := newTestApp(t)
	first := ProcessIdentity{PID: 1, Exe: a.paths.ServerExe, Started: time.Unix(10, 0)}
	probe := &fakeProcessProbe{identity: first}
	starts := 0
	a.server = newServerManager(a.paths, a.events, probe, func(Paths) (launchedProcess, error) {
		starts++
		probe.setErr(nil)
		probe.setIdentity(ProcessIdentity{PID: 2, Exe: a.paths.ServerExe, Started: time.Unix(20, 0)})
		return &fakeLaunchedProcess{pid: 2}, nil
	})
	a.server.processPollInterval = time.Millisecond
	a.server.shutdownTimeout = 2 * time.Second
	writeRecordedProcess(t, a.paths, persistedProcess{PID: first.PID, Exe: first.Exe, Started: first.Started})
	commands := scriptTelnet(t, a, func(command string) string {
		if command == "shutdown" {
			probe.setErr(os.ErrProcessDone)
		}
		return ""
	})
	task := ScheduledTask{Action: TaskRestart, Warnings: []int{3, 1}, Command: "Restart in {minutes}"}
	if err := a.runScheduledTask(context.Background(), task, time.Now().Add(4*warningUnit)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(commands(), "|"); got != `say "Restart in 3"|say "Restart in 1"|shutdown` {
		t.Fatalf("commands = %s", got)
	}
	if starts != 1 {
		t.Fatalf("starts = %d", starts)
	}
}
