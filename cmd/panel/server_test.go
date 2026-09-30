package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownStopsAfterCommandWriteAndDisconnectWhenProcessExits(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout = 100 * time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Started Telnet session.\r\n"))
		buf := make([]byte, len("shutdown\r\n"))
		_, _ = io.ReadFull(peer, buf)
		probe.setErr(os.ErrProcessDone)
		_ = peer.Close()
	}()
	if err := m.Shutdown(context.Background(), TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}); err != nil {
		t.Fatalf("Shutdown()=%v", err)
	}
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("state=%q", got)
	}
}

func TestShutdownWakeupBecomesTerminalFailureAndStaysLocked(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, staysAlive: true}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout, m.pollInterval = 200*time.Millisecond, time.Millisecond
	// The game answers a poll that was in flight when shutdown was sent: the
	// poll returns only after the shutdown command arrived, so the wake-up is
	// seen deterministically rather than by a lucky race.
	sent := make(chan struct{})
	m.poll = func(context.Context) (string, error) { <-sent; return "0", nil }
	m.StartPoller()
	defer m.StopPoller()
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Welcome> "))
		buf := make([]byte, len("shutdown\r\n"))
		_, _ = io.ReadFull(peer, buf)
		close(sent)
		_ = peer.Close()
	}()
	if err := m.Shutdown(context.Background(), TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("Shutdown()=%v", err)
	}
	status := m.Status()
	if status.State != ServerShutdownFailed || status.ForceStopToken == "" {
		t.Fatalf("status=%+v", status)
	}
	requireStatusDiagnostic(t, status, "正常关闭未完成")
	if got := m.Status().State; got != ServerShutdownFailed {
		t.Fatalf("refresh state=%q", got)
	}
	if err := m.Start(); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("Start()=%v", err)
	}
	if release, err := m.BeginStoppedOperation(); !errors.Is(err, ErrServerRunning) || release != nil {
		t.Fatalf("BeginStoppedOperation() release=%t err=%v", release != nil, err)
	}
}

func TestShutdownKeepsImmutableTargetWhenProcessRecordChanges(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, staysAlive: true}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout, m.forceStopTimeout = 20*time.Millisecond, 20*time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	recorded := make(chan error, 1)
	go func() {
		_, _ = peer.Write([]byte("Welcome> "))
		buf := make([]byte, len("shutdown\r\n"))
		_, _ = io.ReadFull(peer, buf)
		recorded <- saveProcess(paths, persistedProcess{PID: 43, Exe: `C:\\server.exe`, Started: started.Add(time.Second)})
		_ = peer.Close()
	}()
	if err := m.Shutdown(context.Background(), TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("Shutdown()=%v", err)
	}
	if err := <-recorded; err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	if status.State != ServerStopTimeout || status.PID != 42 || status.ForceStopToken == "" {
		t.Fatalf("status=%+v", status)
	}
	if err := m.RequestForceStop(status.ForceStopToken); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("RequestForceStop()=%v", err)
	}
	if probe.terminatedProcess.PID != 42 || !probe.terminatedProcess.Started.Equal(started) {
		t.Fatalf("terminated target=%+v", probe.terminatedProcess)
	}
	if err := m.RequestForceStop(status.ForceStopToken); !errors.Is(err, ErrForceStopConfirmation) {
		t.Fatalf("replayed token=%v", err)
	}
}

func TestShutdownIdentityMismatchNeverTerminatesReplacement(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	client, peer := net.Pipe()
	defer peer.Close()
	replaced := make(chan struct{})
	recorded := make(chan error, 1)
	go func() {
		_, _ = peer.Write([]byte("Welcome> "))
		buf := make([]byte, len("shutdown\r\n"))
		_, _ = io.ReadFull(peer, buf)
		probe.setIdentity(ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started.Add(time.Second)})
		recorded <- saveProcess(paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started.Add(time.Second)})
		close(replaced)
		_ = peer.Close()
	}()
	if err := m.Shutdown(context.Background(), TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}); err != nil {
		t.Fatalf("Shutdown()=%v", err)
	}
	<-replaced
	if err := <-recorded; err != nil {
		t.Fatal(err)
	}
	if probe.terminatedCount() != 0 {
		t.Fatalf("replacement terminated=%d", probe.terminatedCount())
	}
	if got := m.Status(); got.State != ServerRunning || got.PID != 42 {
		t.Fatalf("reattached status=%+v", got)
	}
}

func TestStartPollerReattachesOnlyExactPersistedProcess(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.poll = nil
	m.StartPoller()
	m.mu.Lock()
	state, polling, tailing := m.state, m.pollCancel != nil, m.tailCancel != nil
	m.mu.Unlock()
	m.StopPoller()
	if state != ServerRunning || !polling || !tailing {
		t.Fatalf("state=%q polling=%v tailing=%v", state, polling, tailing)
	}
	probe.setIdentity(ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started.Add(time.Second)})
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("stale record state=%q", got)
	}
	if _, err := loadProcess(paths); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale record remains: %v", err)
	}
}

func TestStopPollerPreservesLiveGameRecord(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.StartPoller()
	m.StopPoller()
	if _, err := loadProcess(paths); err != nil {
		t.Fatalf("live record removed: %v", err)
	}
	if probe.terminatedCount() != 0 {
		t.Fatalf("game terminated=%d", probe.terminatedCount())
	}
}

func TestOnlinePlayersClearWhenPollCannotConfirmCount(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.SetOnlinePlayers(3)

	for name, poll := range map[string]func(context.Context) (string, error){
		"failure":     func(context.Context) (string, error) { return "", errors.New("offline") },
		"unparseable": func(context.Context) (string, error) { return "not a player count", nil },
	} {
		t.Run(name, func(t *testing.T) {
			m.SetOnlinePlayers(3)
			m.poll = poll
			m.pollOnce(context.Background())
			if got := m.Status().OnlinePlayers; got != nil {
				t.Fatalf("online players=%d, want unavailable", *got)
			}
		})
	}
}

func TestOnlinePlayersClearWhenServerStops(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.SetOnlinePlayers(3)
	probe.setErr(os.ErrProcessDone)

	status := m.Status()
	if status.State != ServerCrashed || status.OnlinePlayers != nil {
		t.Fatalf("status=%+v, want crashed with unavailable players", status)
	}
}

func TestServerRefreshSettlesUnexpectedCrashOnFollowingRefresh(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.diagnostic = "stale diagnostic"
	m.SetOnlinePlayers(3)
	probe.setErr(os.ErrProcessDone)

	if status := m.Status(); status.State != ServerCrashed {
		t.Fatalf("first refresh=%+v, want crashed", status)
	}
	if status := m.Status(); status.State != ServerStopped || status.Diagnostic != "" || status.OnlinePlayers != nil || status.LastCrash == nil {
		t.Fatalf("second refresh=%+v, want stopped without stale data but with the crash time", status)
	}
	if crashed, stopped := countStateEvents(m.events, ServerCrashed), countStateEvents(m.events, ServerStopped); crashed != 1 || stopped != 1 {
		t.Fatalf("crashed=%d stopped=%d events=%+v", crashed, stopped, m.events.events)
	}
}

func TestCanceledPollCannotRestoreOnlinePlayersAfterStop(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	entered, release := make(chan struct{}), make(chan struct{})
	m.poll = func(context.Context) (string, error) {
		close(entered)
		<-release
		return "Total of 7 in the game", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- m.pollOnce(ctx) }()
	<-entered
	if _, err := m.beginShutdown(); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	<-done
	if got := m.Status().OnlinePlayers; got != nil {
		t.Fatalf("online players=%d, want unavailable after canceled poll", *got)
	}
}

func TestShutdownRejectsConcurrentOperation(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout = 100 * time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	commandSent := make(chan struct{})
	go func() {
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, 64)
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Welcome> "))
		_, _ = peer.Read(buf)
		close(commandSent)
		_, _ = peer.Write([]byte("accepted"))
		<-time.After(time.Second)
	}()
	telnet := TelnetClient{Password: "secret", CommandIdle: time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	first := make(chan error, 1)
	go func() { first <- m.Shutdown(context.Background(), telnet) }()
	<-commandSent
	if err := m.Shutdown(context.Background(), telnet); !errors.Is(err, ErrShutdownInProgress) {
		t.Fatalf("second Shutdown()=%v", err)
	}
	if err := <-first; !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("first Shutdown()=%v", err)
	}
}

func TestShutdownCommandFailureRestartsWatchers(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.poll = nil
	m.StartPoller()
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, len("secret\r\n"))
		_, _ = io.ReadFull(peer, buf)
		_, _ = peer.Write([]byte("Welcome> "))
		_ = peer.Close()
	}()
	err := m.Shutdown(context.Background(), TelnetClient{Password: "secret", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }})
	if err == nil {
		t.Fatal("Shutdown() succeeded after disconnect before command write")
	}
	m.mu.Lock()
	state, tailing := m.state, m.tailCancel != nil
	m.mu.Unlock()
	if state != ServerRunning || !tailing {
		t.Fatalf("state=%q tailing=%v", state, tailing)
	}
	m.StopPoller()
}

func TestBeginShutdownStopsPollButKeepsLogTail(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.poll = nil
	m.StartPoller()
	target, err := m.beginShutdown()
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	polling, tailing := m.pollCancel != nil, m.tailCancel != nil
	m.mu.Unlock()
	if polling || !tailing {
		t.Fatalf("shutdown watchers: polling=%v tailing=%v", polling, tailing)
	}
	m.cancelShutdown(target.Operation)
	m.StopPoller()
}

func TestShutdownDoesNotOfferForceStopTokenBeforeTimeout(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, staysAlive: true}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.Shutdown(ctx, TelnetClient{Password: "secret", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		status := m.Status()
		if status.State == ServerStopping {
			if status.ForceStopToken != "" {
				t.Fatalf("force-stop token offered before timeout: %q", status.ForceStopToken)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shutdown state=%q, want stopping", status.State)
		}
		time.Sleep(time.Millisecond)
	}
	_ = peer.Close()
	if err := <-done; err == nil {
		t.Fatal("Shutdown() succeeded after login connection closed")
	}
}

func TestShutdownTimeoutTreatsDelayedExitAsStopped(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, staysAlive: true}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout = 20 * time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, 64)
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Welcome> "))
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("accepted"))
	}()
	err := m.Shutdown(context.Background(), TelnetClient{Password: "secret", CommandIdle: time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }})
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("Shutdown()=%v", err)
	}
	if got := m.Status().State; got != ServerStopTimeout {
		t.Fatalf("timeout state=%q", got)
	}
	if token := m.Status().ForceStopToken; token == "" {
		t.Fatal("stop timeout did not offer a one-time force-stop token")
	}
	probe.setErr(os.ErrProcessDone)
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("delayed exit state=%q", got)
	}
	if countStateEvents(m.events, ServerCrashed) != 0 || countStateEvents(m.events, ServerStopped) != 1 {
		t.Fatalf("events=%+v", m.events.events)
	}
}

func TestStartRejectsExitedProcessWhileShutdownOwnerFinishes(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	paths := ResolvePaths(t.TempDir())
	starts := 0
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { starts++; return &fakeLaunchedProcess{pid: 43}, nil })
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.mu.Lock()
	m.shutdownActive, m.shutdownOperation, m.state = true, 1, ServerStopping
	m.mu.Unlock()
	probe.setErr(os.ErrProcessDone)
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("state=%q", got)
	}
	if err := m.Start(); !errors.Is(err, ErrShutdownInProgress) {
		t.Fatalf("Start()=%v", err)
	}
	if starts != 0 {
		t.Fatalf("starter calls=%d", starts)
	}
	m.finishShutdown(1, ServerStopped)
	m.mu.Lock()
	active := m.shutdownActive
	m.mu.Unlock()
	if active {
		t.Fatal("shutdown owner did not finish")
	}
}

func TestStartRejectsActiveStoppedOperationLease(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	starts := 0
	m := newServerManager(paths, nil, &fakeProcessProbe{}, func(Paths) (launchedProcess, error) {
		starts++
		return &fakeLaunchedProcess{pid: 42}, nil
	})
	release, err := m.BeginStoppedOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); !errors.Is(err, ErrServerMaintenance) {
		t.Fatalf("Start() error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("starter calls = %d", starts)
	}
	release()
}

func TestConfigMutationAndStoppedOperationAreMutuallyExclusive(t *testing.T) {
	m := newServerManager(ResolvePaths(t.TempDir()), nil, &fakeProcessProbe{}, nil)
	releaseConfig, err := m.BeginConfigMutation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginStoppedOperation(); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("stopped operation during config mutation = %v", err)
	}
	releaseConfig()

	releaseMaintenance, err := m.BeginStoppedOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginConfigMutation(); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("config mutation during maintenance = %v", err)
	}
	releaseMaintenance()
}

func TestRequireRunningRefreshesExactRecordedIdentity(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	if err := m.RequireRunning(); err != nil {
		t.Fatalf("matching process = %v", err)
	}
	probe.setIdentity(ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started.Add(time.Second)})
	if err := m.RequireRunning(); !errors.Is(err, ErrServerStopped) {
		t.Fatalf("reused PID = %v", err)
	}
}

func TestOnlinePollerRunsOnlyWhileVerifiedRunning(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.pollInterval = 10 * time.Millisecond
	calls := make(chan time.Time, 8)
	m.poll = func(context.Context) (string, error) { calls <- time.Now(); return "Total of 12 in the game", nil }
	m.StartPoller()
	first := <-calls
	second := <-calls
	if second.Sub(first) < m.pollInterval {
		t.Fatalf("poll interval=%v", second.Sub(first))
	}
	if got := m.Status().OnlinePlayers; got == nil || *got != 12 {
		t.Fatalf("online=%v", got)
	}
	probe.setErr(os.ErrProcessDone)
	m.Status()
	select {
	case call := <-calls:
		t.Fatalf("poll after stop at %v", call)
	case <-time.After(30 * time.Millisecond):
	}
	m.StopPoller()
}

func TestNonStatusRefreshPublishesCrashExactlyOnce(t *testing.T) {
	for name, refresh := range map[string]func(*ServerManager) error{
		"poller":            func(m *ServerManager) error { m.StartPoller(); return nil },
		"stopped operation": func(m *ServerManager) error { _, err := m.BeginStoppedOperation(); return err },
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Unix(10, 0)
			probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
			m, paths := testManager(t, probe)
			writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
			if got := m.Status().State; got != ServerRunning {
				t.Fatalf("initial state=%q", got)
			}
			probe.setErr(os.ErrProcessDone)
			_ = refresh(m)
			if got := countStateEvents(m.events, ServerCrashed); got != 1 {
				t.Fatalf("crashed events=%d events=%+v", got, m.events.events)
			}
		})
	}
}

func countStateEvents(events *EventBroker, state ServerState) int {
	events.mu.Lock()
	defer events.mu.Unlock()
	count := 0
	for _, event := range events.events {
		if event.Type == "server" && eventDataState(event) == string(state) {
			count++
		}
	}
	return count
}

func TestServerLogTailPublishesLinesAndStopsAfterCrash(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.poll = nil
	m.tailInterval = 5 * time.Millisecond
	ch, cancel, _ := m.events.subscribe()
	defer cancel()
	m.StartPoller()
	logPath := filepath.Join(paths.Logs, "server-current.log")
	if err := os.WriteFile(logPath, []byte("first live line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, func(event Event) bool {
		return event.Type == "log" && event.Source == "server" && event.Message == "first live line"
	})
	probe.setErr(os.ErrProcessDone)
	// The poller may read the status first and see "crashed" itself; either
	// way the crash must be reported and stay visible afterwards.
	if status := m.Status(); (status.State != ServerCrashed && status.State != ServerStopped) || status.LastCrash == nil {
		t.Fatalf("status after crash = %+v", status)
	}
	if countStateEvents(m.events, ServerCrashed) != 1 {
		t.Fatalf("crashed events=%d", countStateEvents(m.events, ServerCrashed))
	}
	if err := os.WriteFile(logPath, []byte("first live line\nafter crash\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(40 * time.Millisecond)
	for {
		select {
		case event := <-ch:
			if event.Message == "after crash" {
				t.Fatal("tailer published after crash")
			}
		case <-deadline:
			return
		}
	}
}

func TestServerBuildIsCachedAndUpdatedByLogTail(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(paths.Logs, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(paths.Logs, "server-current.log")
	if err := os.WriteFile(logPath, []byte("x INF Version: Alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newServerManager(paths, NewEventBroker(), probe, nil)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	if got := m.Status().Build; got != "Alpha" {
		t.Fatalf("initial build=%q", got)
	}
	if err := os.WriteFile(logPath, []byte("x INF Version: ChangedWithoutTail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := m.Status().Build; got != "Alpha" {
		t.Fatalf("Status reread log, build=%q", got)
	}
	if err := os.WriteFile(logPath, []byte("x INF Version: Alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.poll = nil
	m.tailInterval = 5 * time.Millisecond
	ch, cancel, _ := m.events.subscribe()
	defer cancel()
	m.StartPoller()
	defer m.StopPoller()
	waitForEvent(t, ch, func(event Event) bool { return event.Type == "log" && strings.Contains(event.Message, "Alpha") })
	time.Sleep(2 * m.tailInterval)
	newLog := "new head INF Version: BetaBuildMuchLongerThanAlpha\nnew tail line\n"
	if err := os.WriteFile(logPath, []byte(newLog), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for m.Status().Build != "BetaBuildMuchLongerThanAlpha" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := m.Status().Build; got != "BetaBuildMuchLongerThanAlpha" {
		t.Fatalf("tailed build=%q", got)
	}
	waitForEvent(t, ch, func(event Event) bool {
		return event.Type == "log" && event.Message == "new head INF Version: BetaBuildMuchLongerThanAlpha"
	})
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("x INF Version: Gamma\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for m.Status().Build != "Gamma" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := m.Status().Build; got != "Gamma" {
		t.Fatalf("rotated build=%q", got)
	}
}

func TestParseServerBuildCanonicalizesOfficialVersionLine(t *testing.T) {
	line := "2026-08-11T00:00:00 INF Version: V 3.1.0 (b14) Compatibility Version: V 3.1.0"
	got, ok := parseServerBuild(line)
	if !ok || got != "V3.1.0-b14" {
		t.Fatalf("parseServerBuild() = %q, %v", got, ok)
	}
}

func TestParseServerBuildDoesNotCanonicalizeEmbeddedOrMalformedOfficialValues(t *testing.T) {
	for _, line := range []string{
		"x INF Version: experimental V 3.1.0 (b14)",
		"x INF Version: V 3.1.0 (b14) trailing",
	} {
		got, ok := parseServerBuild(line)
		if !ok || got != strings.TrimSpace(strings.TrimPrefix(line, "x INF Version:")) {
			t.Fatalf("parseServerBuild(%q) = %q, %v", line, got, ok)
		}
	}
}

func TestParseServerBuildKeepsUnknownFallbackWithoutCompatibilitySuffix(t *testing.T) {
	got, ok := parseServerBuild("x INF Version: Experimental Compatibility Version: old")
	if !ok || got != "Experimental" {
		t.Fatalf("parseServerBuild() = %q, %v", got, ok)
	}
}

func waitForEvent(t *testing.T, ch <-chan Event, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-ch:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

type fakeProcessProbe struct {
	mu         sync.RWMutex
	identity   ProcessIdentity
	err        error
	terminated int
}

func (p *fakeProcessProbe) Lookup(int) (ProcessIdentity, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.identity, p.err
}
func (p *fakeProcessProbe) setErr(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}
func (p *fakeProcessProbe) setIdentity(identity ProcessIdentity) {
	p.mu.Lock()
	p.identity = identity
	p.mu.Unlock()
}
func (p *fakeProcessProbe) terminatedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.terminated
}

type verifiedFakeProcessProbe struct {
	*fakeProcessProbe
	staysAlive        bool
	postTerminateErr  error
	terminatedProcess persistedProcess
}

func (p *verifiedFakeProcessProbe) TerminateVerified(process persistedProcess) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated++
	p.terminatedProcess = process
	if !p.staysAlive {
		p.err = p.postTerminateErr
		if p.err == nil {
			p.err = os.ErrProcessDone
		}
	}
	return nil
}

type blockingVerifiedProcessProbe struct {
	identity ProcessIdentity
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	exited   bool
	calls    int
}

func (p *blockingVerifiedProcessProbe) Lookup(int) (ProcessIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	return p.identity, nil
}
func (p *blockingVerifiedProcessProbe) TerminateVerified(persistedProcess) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.started <- struct{}{}
	<-p.release
	p.mu.Lock()
	p.exited = true
	p.mu.Unlock()
	return nil
}
func (p *blockingVerifiedProcessProbe) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type blockingLaunchedProcess struct {
	pid     int
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (p *blockingLaunchedProcess) PID() int { return p.pid }
func (p *blockingLaunchedProcess) Kill() error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.started <- struct{}{}
	<-p.release
	return nil
}
func (p *blockingLaunchedProcess) Wait() error    { return nil }
func (p *blockingLaunchedProcess) Release() error { return nil }
func (p *blockingLaunchedProcess) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakeLaunchedProcess struct {
	pid                      int
	killed, waited, released atomic.Int32
	killErr                  error
	waitErr                  error
	waitBlock                chan struct{}
}

func (p *fakeLaunchedProcess) PID() int    { return p.pid }
func (p *fakeLaunchedProcess) Kill() error { p.killed.Add(1); return p.killErr }
func (p *fakeLaunchedProcess) Wait() error {
	p.waited.Add(1)
	if p.waitBlock != nil {
		<-p.waitBlock
	}
	return p.waitErr
}
func (p *fakeLaunchedProcess) Release() error { p.released.Add(1); return nil }

func writeRecordedProcess(t *testing.T, paths Paths, process persistedProcess) {
	t.Helper()
	if err := os.MkdirAll(paths.Logs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveProcess(paths, process); err != nil {
		t.Fatal(err)
	}
}

func testManager(t *testing.T, probe ProcessProbe) (*ServerManager, Paths) {
	t.Helper()
	paths := ResolvePaths(t.TempDir())
	return newServerManager(paths, NewEventBroker(), probe, nil), paths
}

func waitForLaunched(t *testing.T, process *fakeLaunchedProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for process.waited.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if process.waited.Load() != 1 {
		t.Fatalf("waited=%d", process.waited.Load())
	}
}

func TestServerStartPersistsVerifiedIdentity(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: paths.ServerExe, Started: started}}
	launched := &fakeLaunchedProcess{pid: 42}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return launched, nil })
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := loadProcess(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 42 || got.Exe != paths.ServerExe || !got.Started.Equal(started) {
		t.Fatalf("process=%+v", got)
	}
	if launched.released.Load() != 1 {
		t.Fatalf("released=%d", launched.released.Load())
	}
}

func TestServerStartClearsPreviousOnlinePlayersBeforeFirstPoll(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: paths.ServerExe, Started: started}}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return &fakeLaunchedProcess{pid: 42}, nil })
	m.poll = nil
	m.SetOnlinePlayers(3)

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.StopPoller()
	if got := m.Status().OnlinePlayers; got != nil {
		t.Fatalf("online players=%d, want unavailable before first poll", *got)
	}
}

func TestServerStartCheckFailureLeavesStateStopped(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	starts := 0
	blocked := errors.New("blocked")
	m := newServerManager(paths, nil, &fakeProcessProbe{}, func(Paths) (launchedProcess, error) {
		starts++
		return nil, nil
	})
	m.startCheck = func() error { return blocked }
	if err := m.Start(); !errors.Is(err, blocked) {
		t.Fatalf("err=%v", err)
	}
	if starts != 0 || m.Status().State != ServerStopped {
		t.Fatalf("starts=%d state=%q", starts, m.Status().State)
	}
}

func TestServerStartCleansLaunchedProcessWhenVerificationFails(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	launched := &fakeLaunchedProcess{pid: 42}
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\other.exe`, Started: time.Unix(10, 0)}}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return launched, nil })
	if err := m.Start(); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if launched.killed.Load() != 1 {
		t.Fatalf("killed=%d", launched.killed.Load())
	}
	waitForLaunched(t, launched)
}

func TestServerStartCleansLaunchedProcessWhenPersistenceFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("relies on Windows reporting a missing path below a regular file")
	}
	paths := ResolvePaths(t.TempDir())
	launched := &fakeLaunchedProcess{pid: 42}
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: paths.ServerExe, Started: started}}
	if err := os.WriteFile(paths.Logs, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return launched, nil })
	if err := m.Start(); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if launched.killed.Load() != 1 {
		t.Fatalf("killed=%d", launched.killed.Load())
	}
	waitForLaunched(t, launched)
}

func TestServerRetainsFailedLaunchWithoutBlockingAndForceStopsExactHandle(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	launched := &fakeLaunchedProcess{pid: 42, killErr: errors.New("kill failed"), waitErr: errors.New("killed"), waitBlock: make(chan struct{})}
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\other.exe`, Started: time.Unix(10, 0)}}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return launched, nil })
	done := make(chan error, 1)
	go func() { done <- m.Start() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		close(launched.waitBlock)
		t.Fatal("Start blocked on failed cleanup")
	}
	status := m.Status()
	if status.State != ServerCrashed || status.ForceStopToken == "" {
		t.Fatalf("status=%+v", status)
	}
	if err := m.Start(); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("Start after failed cleanup=%v", err)
	}
	if launched.waited.Load() != 0 {
		t.Fatalf("Wait called after Kill failure: %d", launched.waited.Load())
	}
	launched.killErr = nil
	forced := make(chan error, 1)
	go func() { forced <- m.RequestForceStop(status.ForceStopToken) }()
	select {
	case err := <-forced:
		t.Fatalf("force stop returned before process exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(launched.waitBlock)
	if err := <-forced; err != nil {
		t.Fatal(err)
	}
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("state=%q", got)
	}
	if launched.killed.Load() != 2 || launched.waited.Load() != 1 {
		t.Fatalf("cleanup killed=%d waited=%d", launched.killed.Load(), launched.waited.Load())
	}
}

func TestServerRefreshStopsReusedPIDWithDifferentExecutable(t *testing.T) {
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\other.exe`, Started: time.Unix(10, 0)}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: time.Unix(10, 0)})
	if got := m.Status(); got.State != ServerStopped {
		t.Fatalf("state=%q", got.State)
	}
	if _, err := os.Stat(filepath.Join(paths.Logs, "server-process.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file err=%v", err)
	}
}

func TestServerRefreshStopsReusedPIDWithDifferentStartTime(t *testing.T) {
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: time.Unix(11, 0)}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: time.Unix(10, 0)})
	if got := m.Status(); got.State != ServerStopped {
		t.Fatalf("state=%q", got.State)
	}
}

func TestServerRefreshKeepsStoppingWhenProcessProbeFails(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}, err: errors.New("access denied")}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.state, m.shutdownActive, m.exitExpected = ServerStopping, true, true

	status := m.Status()
	if status.State != ServerStopping || status.PID != 42 {
		t.Fatalf("status=%+v", status)
	}
	requireStatusDiagnostic(t, status, "access denied")
	if _, err := loadProcess(paths); err != nil {
		t.Fatalf("process record removed after probe failure: %v", err)
	}
}

func TestServerRefreshDoesNotReportStoppedWhenInitialProcessProbeFails(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}, err: errors.New("access denied")}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})

	status := m.Status()
	if status.State != ServerStopTimeout || status.PID != 42 {
		t.Fatalf("status=%+v", status)
	}
	if release, err := m.BeginStoppedOperation(); !errors.Is(err, ErrServerRunning) {
		if release != nil {
			release()
		}
		t.Fatalf("BeginStoppedOperation()=%v", err)
	}
}

func TestServerRefreshKeepsStoppingWhenProcessRecordIsUnreadable(t *testing.T) {
	m, paths := testManager(t, &fakeProcessProbe{})
	if err := os.MkdirAll(paths.Logs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(processPath(paths), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	m.state, m.shutdownActive, m.exitExpected = ServerStopping, true, true

	status := m.Status()
	if status.State != ServerStopping {
		t.Fatalf("status=%+v", status)
	}
	requireStatusDiagnostic(t, status, "invalid character")
	if got, err := os.ReadFile(processPath(paths)); err != nil || string(got) != "not json" {
		t.Fatalf("process record changed after read failure: %q, %v", got, err)
	}
}

func requireStatusDiagnostic(t *testing.T, status ServerStatus, contains string) {
	t.Helper()
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	got, _ := payload["diagnostic"].(string)
	if !strings.Contains(got, contains) {
		t.Fatalf("diagnostic=%q, want %q in %s", got, contains, b)
	}
}

func TestServerUnexpectedVerifiedExitCrashesAndCanRestart(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	if got := m.Status().State; got != ServerRunning {
		t.Fatalf("initial state=%q", got)
	}
	ch, cancel, _ := m.events.subscribe()
	defer cancel()
	probe.setErr(os.ErrProcessDone)
	if got := m.Status().State; got != ServerCrashed {
		t.Fatalf("state=%q", got)
	}
	status := m.Status()
	if status.ForceStopToken != "" {
		t.Fatalf("unexpected crash force token=%q", status.ForceStopToken)
	}
	if err := m.RequestForceStop("anything"); !errors.Is(err, ErrForceStopConfirmation) {
		t.Fatalf("force stop crashed process=%v", err)
	}
	waitForEvent(t, ch, func(event Event) bool {
		return event.Type == "server" && strings.Contains(eventDataState(event), string(ServerCrashed))
	})
	probe.setErr(nil)
	probe.setIdentity(ProcessIdentity{PID: 43, Exe: paths.ServerExe, Started: started.Add(time.Second)})
	m.start = func(Paths) (launchedProcess, error) { return &fakeLaunchedProcess{pid: 43}, nil }
	if err := m.Start(); err != nil {
		t.Fatalf("Start after crash=%v", err)
	}
}

func eventDataState(event Event) string {
	data, _ := event.Data.(map[string]string)
	return data["state"]
}

func TestServerStartRejectsMatchingRecordedProcess(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	if err := m.Start(); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("Start() error=%v", err)
	}
}

func TestServerRunningOffersOneTimeVerifiedForceStop(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	status := m.Status()
	if status.State != ServerRunning || status.ForceStopToken == "" {
		t.Fatalf("status=%+v", status)
	}
	if err := m.RequestForceStop(status.ForceStopToken); err != nil {
		t.Fatal(err)
	}
	if probe.terminatedCount() != 1 || m.Status().State != ServerStopped {
		t.Fatalf("terminated=%d status=%+v", probe.terminatedCount(), m.Status())
	}
	if _, err := loadProcess(paths); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process record remains: %v", err)
	}
	if err := m.RequestForceStop(status.ForceStopToken); !errors.Is(err, ErrForceStopConfirmation) {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestServerForceStopTokenCoversLiveStoppingTimeoutAndCrashedStates(t *testing.T) {
	for _, state := range []ServerState{ServerStopping, ServerStopTimeout, ServerCrashed} {
		t.Run(string(state), func(t *testing.T) {
			started := time.Unix(10, 0)
			probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}}
			m, paths := testManager(t, probe)
			writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
			m.state = state
			status := m.Status()
			if status.State != state || status.ForceStopToken == "" {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestServerStopTimeoutForceStopRequiresCurrentOneTimeToken(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.state = ServerStopTimeout
	status := m.Status()
	if status.ForceStopToken == "" {
		t.Fatal("missing force-stop token")
	}
	if err := m.RequestForceStop("wrong"); !errors.Is(err, ErrForceStopConfirmation) {
		t.Fatalf("wrong token error=%v", err)
	}
	if err := m.RequestForceStop(status.ForceStopToken); err != nil {
		t.Fatal(err)
	}
	if probe.terminatedCount() != 1 {
		t.Fatalf("terminated=%d", probe.terminatedCount())
	}
	if err := m.RequestForceStop(status.ForceStopToken); !errors.Is(err, ErrForceStopConfirmation) {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestServerForceStopWaitsForVerifiedExit(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, staysAlive: true}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.state = ServerStopTimeout
	m.forceStopTimeout = 20 * time.Millisecond
	m.processPollInterval = 2 * time.Millisecond
	token := m.Status().ForceStopToken
	if err := m.RequestForceStop(token); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("RequestForceStop()=%v", err)
	}
	status := m.Status()
	if status.State != ServerStopTimeout || status.ForceStopToken == "" {
		t.Fatalf("status=%+v", status)
	}
	if _, err := loadProcess(paths); err != nil {
		t.Fatalf("process record removed before exit: %v", err)
	}
	if err := m.Start(); !errors.Is(err, ErrServerRunning) {
		t.Fatalf("Start while process alive=%v", err)
	}
	probe.setErr(os.ErrProcessDone)
	if got := m.Status().State; got != ServerStopped {
		t.Fatalf("state after confirmed exit=%q", got)
	}
}

func TestServerForceStopProbeFailureDoesNotConfirmExit(t *testing.T) {
	started := time.Unix(10, 0)
	probeErr := errors.New("access denied")
	probe := &verifiedFakeProcessProbe{fakeProcessProbe: &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}, postTerminateErr: probeErr}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.forceStopTimeout = 20 * time.Millisecond
	m.processPollInterval = 2 * time.Millisecond

	err := m.RequestForceStop(m.Status().ForceStopToken)
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("RequestForceStop()=%v", err)
	}
	status := m.Status()
	if status.State != ServerStopTimeout {
		t.Fatalf("status=%+v", status)
	}
	requireStatusDiagnostic(t, status, probeErr.Error())
	if _, err := loadProcess(paths); err != nil {
		t.Fatalf("process record removed without exit proof: %v", err)
	}
}

func TestServerForceStopIsSingleFlightWhileTerminationIsInProgress(t *testing.T) {
	started := time.Unix(10, 0)
	t.Run("persisted", func(t *testing.T) {
		probe := &blockingVerifiedProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}, started: make(chan struct{}, 2), release: make(chan struct{})}
		m, paths := testManager(t, probe)
		writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
		token := m.Status().ForceStopToken
		first := make(chan error, 1)
		go func() { first <- m.RequestForceStop(token) }()
		<-probe.started
		released := false
		defer func() {
			if !released {
				close(probe.release)
			}
		}()
		if status := m.Status(); status.ForceStopToken != "" {
			t.Fatalf("Status minted token during force stop: %+v", status)
		}
		if status := m.Status(); status.ForceStopToken != "" {
			t.Fatalf("Refresh minted token during force stop: %+v", status)
		}
		second := make(chan error, 1)
		go func() { second <- m.RequestForceStop(m.Status().ForceStopToken) }()
		select {
		case err := <-second:
			if !errors.Is(err, ErrForceStopConfirmation) {
				t.Fatalf("second force stop=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("second force stop started another termination")
		}
		if calls := probe.callCount(); calls != 1 {
			t.Fatalf("terminations=%d", calls)
		}
		close(probe.release)
		released = true
		if err := <-first; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("retained launch", func(t *testing.T) {
		launched := &blockingLaunchedProcess{pid: 42, started: make(chan struct{}, 2), release: make(chan struct{})}
		m, _ := testManager(t, &fakeProcessProbe{})
		m.launched, m.state, m.forceToken = launched, ServerCrashed, "once"
		first := make(chan error, 1)
		go func() { first <- m.RequestForceStop("once") }()
		<-launched.started
		released := false
		defer func() {
			if !released {
				close(launched.release)
			}
		}()
		if status := m.Status(); status.ForceStopToken != "" {
			t.Fatalf("Status minted token during force stop: %+v", status)
		}
		if status := m.Status(); status.ForceStopToken != "" {
			t.Fatalf("Refresh minted token during force stop: %+v", status)
		}
		second := make(chan error, 1)
		go func() { second <- m.RequestForceStop(m.Status().ForceStopToken) }()
		select {
		case err := <-second:
			if !errors.Is(err, ErrForceStopConfirmation) {
				t.Fatalf("second force stop=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("second force stop started another termination")
		}
		if calls := launched.callCount(); calls != 1 {
			t.Fatalf("kills=%d", calls)
		}
		close(launched.release)
		released = true
		if err := <-first; err != nil {
			t.Fatal(err)
		}
	})
}

func TestServerForceStopRejectsProbeWithoutVerifiedTermination(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.state = ServerStopTimeout
	if token := m.Status().ForceStopToken; token != "" {
		t.Fatalf("unverified probe exposed token %q", token)
	}
	if err := m.RequestForceStop(m.Status().ForceStopToken); !errors.Is(err, ErrVerifiedTerminationRequired) {
		t.Fatalf("force stop error=%v", err)
	}
	if probe.terminatedCount() != 0 {
		t.Fatal("unverified probe received bare terminate")
	}
}

func TestServerArgsSeparateLogFilePath(t *testing.T) {
	paths := ResolvePaths(`F:\7dtd panel`)
	args := serverArgs(paths)
	wantLog := filepath.Join(paths.Logs, "server-current.log")
	for i, arg := range args {
		if strings.HasPrefix(arg, "-logfile=") {
			t.Fatalf("joined log argument %q", arg)
		}
		if arg == "-logfile" && i+1 < len(args) && args[i+1] == wantLog {
			return
		}
	}
	t.Fatalf("missing adjacent -logfile and %q in %#v", wantLog, args)
}

func startingManager(t *testing.T) *ServerManager {
	t.Helper()
	paths := ResolvePaths(t.TempDir())
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: paths.ServerExe, Started: time.Unix(10, 0)}}
	m := newServerManager(paths, NewEventBroker(), probe, func(Paths) (launchedProcess, error) { return &fakeLaunchedProcess{pid: 42}, nil })
	m.requireReady = true
	m.poll = nil
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopPoller)
	return m
}

func TestServerStaysStartingUntilWorldIsLoaded(t *testing.T) {
	m := startingManager(t)
	if got := m.Status().State; got != ServerStarting {
		t.Fatalf("state after Start = %q, want starting", got)
	}
	if err := m.RequireRunning(); err == nil {
		t.Fatal("a starting server must not accept a normal stop or Telnet commands")
	}
	if !forceStopState(ServerStarting) {
		t.Fatal("force stop must stay available while starting")
	}
	m.mu.Lock()
	generation := m.tailGeneration
	m.mu.Unlock()
	m.publishLogLine(generation, "2026-01-01T00:00:00 12.3 INF Loading world")
	if got := m.Status().State; got != ServerStarting {
		t.Fatalf("state after an unrelated line = %q", got)
	}
	m.publishLogLine(generation, "2026-01-01T00:00:05 20.1 INF StartGame done")
	if got := m.Status().State; got != ServerRunning {
		t.Fatalf("state after ready marker = %q, want running", got)
	}
}

func TestServerBecomesRunningWhenTelnetAnswers(t *testing.T) {
	m := startingManager(t)
	m.mu.Lock()
	m.poll = func(context.Context) (string, error) { return "Total of 0 in the game", nil }
	m.mu.Unlock()
	m.pollOnce(context.Background())
	if got := m.Status().State; got != ServerRunning {
		t.Fatalf("state after a successful poll = %q, want running", got)
	}
}

func TestServerStartingFallsBackToRunningAfterGrace(t *testing.T) {
	m := startingManager(t)
	m.mu.Lock()
	m.startingSince = time.Now().Add(-m.startupGrace - time.Second)
	m.mu.Unlock()
	if got := m.Status().State; got != ServerRunning {
		t.Fatalf("state after grace = %q, want running", got)
	}
}

func TestRotateServerLogKeepsRecentRuns(t *testing.T) {
	logs := t.TempDir()
	current := filepath.Join(logs, "server-current.log")
	for i := 0; i < 7; i++ {
		if err := os.WriteFile(current, []byte(fmt.Sprintf("run %d\n", i)), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 1, 1, 0, i, 0, 0, time.Local)
		if err := os.Chtimes(current, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if err := rotateServerLog(logs, 5); err != nil {
			t.Fatal(err)
		}
	}
	kept, _ := filepath.Glob(filepath.Join(logs, "server-[0-9]*.log"))
	if len(kept) != 5 || exists(current) {
		t.Fatalf("kept=%v current exists=%v", kept, exists(current))
	}
	last, _ := os.ReadFile(kept[len(kept)-1])
	if string(last) != "run 6\n" {
		t.Fatalf("newest kept log = %q", last)
	}
	if err := rotateServerLog(logs, 5); err != nil {
		t.Fatalf("rotation without a current log: %v", err)
	}
}

func TestLastCrashPersistsUntilNextStart(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	probe.setErr(os.ErrProcessDone)
	for i := 0; i < 3; i++ {
		if m.Status().LastCrash == nil {
			t.Fatalf("read %d lost the crash", i)
		}
	}
	probe.setErr(nil)
	probe.identity = ProcessIdentity{PID: 43, Exe: paths.ServerExe, Started: started}
	m.start = func(Paths) (launchedProcess, error) { return &fakeLaunchedProcess{pid: 43}, nil }
	m.poll = nil
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.StopPoller()
	if status := m.Status(); status.LastCrash != nil {
		t.Fatalf("a new start must clear the crash: %+v", status)
	}
}
