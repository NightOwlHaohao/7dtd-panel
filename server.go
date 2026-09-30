package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ServerState string

const (
	ServerStopped        ServerState = "stopped"
	ServerStarting       ServerState = "starting"
	ServerRunning        ServerState = "running"
	ServerStopping       ServerState = "stopping"
	ServerStopTimeout    ServerState = "stop_timeout"
	ServerShutdownFailed ServerState = "shutdown_failed"
	ServerCrashed        ServerState = "crashed"
)

var (
	ErrServerRunning               = errors.New("server is already running")
	ErrServerStopped               = errors.New("server is not running")
	ErrForceStopConfirmation       = errors.New("force-stop confirmation is invalid")
	ErrVerifiedTerminationRequired = errors.New("force-stop requires verified termination")
	ErrShutdownTimeout             = errors.New("server did not exit after shutdown")
	ErrShutdownInProgress          = errors.New("server shutdown is already in progress")
	ErrServerMaintenance           = errors.New("server maintenance is active")
	ErrServerBusy                  = errors.New("server is busy")
)

type ProcessIdentity struct {
	PID     int
	Exe     string
	Started time.Time
}

type ProcessProbe interface {
	// Lookup returns os.ErrProcessDone only when the exact PID no longer exists.
	Lookup(pid int) (ProcessIdentity, error)
}

type verifiedProcessProbe interface {
	TerminateVerified(persistedProcess) error
}

type launchedProcess interface {
	PID() int
	Kill() error
	Wait() error
	Release() error
}

type persistedProcess struct {
	PID     int       `json:"pid"`
	Exe     string    `json:"exe"`
	Started time.Time `json:"started"`
}

// shutdownTarget is captured before the shutdown command is sent. The process
// record is mutable across panel restarts, so shutdown and force-stop decisions
// must not consult it again while this target is still alive.
type shutdownTarget struct {
	Operation uint64
	Process   persistedProcess
	SentAt    time.Time
}

type ServerStatus struct {
	State          ServerState `json:"state"`
	PID            int         `json:"pid,omitempty"`
	ForceStopToken string      `json:"forceStopToken,omitempty"`
	Diagnostic     string      `json:"diagnostic,omitempty"`
	OnlinePlayers  *int        `json:"onlinePlayers,omitempty"`
	StartedAt      *time.Time  `json:"startedAt,omitempty"`
	// LastCrash is when the previous run exited unexpectedly; it stays set
	// until the next start, because the crashed state itself lasts only
	// until the next status read.
	LastCrash *time.Time `json:"lastCrash,omitempty"`
	World     string     `json:"world"`
	GameName  string     `json:"gameName"`
	Build     string     `json:"build"`
}

type ServerManager struct {
	paths               Paths
	events              *EventBroker
	probe               ProcessProbe
	start               func(Paths) (launchedProcess, error)
	startCheck          func() error
	launched            launchedProcess
	mu                  sync.Mutex
	state               ServerState
	forceToken          string
	diagnostic          string
	forceStopActive     bool
	shutdownTimeout     time.Duration
	onlinePlayers       *int
	shutdownActive      bool
	exitExpected        bool
	maintenanceActive   bool
	maintenanceID       uint64
	configMutations     int
	shutdownOperation   uint64
	shutdownTarget      *shutdownTarget
	forceTarget         persistedProcess
	shutdownWoke        bool
	pollInterval        time.Duration
	poll                func(context.Context) (string, error)
	pollCancel          context.CancelFunc
	tailInterval        time.Duration
	tailCancel          context.CancelFunc
	tailGeneration      uint64
	build               string
	forceStopTimeout    time.Duration
	processPollInterval time.Duration
	// requireReady keeps a started server in ServerStarting until its log
	// reports the world is loaded or Telnet answers, bounded by startupGrace.
	requireReady  bool
	startingSince time.Time
	lastCrash     time.Time
	startupGrace  time.Duration
	infoCache     serverInfoCache
	// pollObserver sees each successful "lp" poll (the player history).
	pollObserver func(output string)
}

// serverReadyMarker is logged by the dedicated server once the world is loaded.
const serverReadyMarker = "StartGame done"

func NewServerManager(paths Paths, events *EventBroker) *ServerManager {
	m := newServerManager(paths, events, newProcessProbe(), startServer)
	m.startCheck = (ModService{paths: paths, server: m}).StartBlocker
	m.requireReady = true
	return m
}

func newServerManager(paths Paths, events *EventBroker, probe ProcessProbe, start func(Paths) (launchedProcess, error)) *ServerManager {
	m := &ServerManager{paths: paths, events: events, probe: probe, start: start, state: ServerStopped, shutdownTimeout: 120 * time.Second, pollInterval: 30 * time.Second, tailInterval: 100 * time.Millisecond, forceStopTimeout: 10 * time.Second, processPollInterval: 100 * time.Millisecond, startupGrace: 10 * time.Minute}
	m.build = scanServerBuild(filepath.Join(paths.Logs, "server-current.log"))
	m.poll = func(ctx context.Context) (string, error) {
		client, err := telnetFromConfig(paths.ServerConfig)
		if err != nil {
			return "", err
		}
		return client.Command(ctx, "lp")
	}
	return m
}

func (m *ServerManager) StartPoller() {
	m.mu.Lock()
	if state := m.refreshLocked().State; state != ServerRunning && state != ServerStarting {
		m.mu.Unlock()
		return
	}
	if m.pollCancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.pollCancel = cancel
		go m.pollLoop(ctx, m.pollInterval)
	}
	if m.tailCancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.tailCancel = cancel
		m.tailGeneration++
		go m.tailLoop(ctx, m.tailInterval, m.tailGeneration)
	}
	m.mu.Unlock()
}

func (m *ServerManager) StopPoller() { m.mu.Lock(); m.stopWatchersLocked(); m.mu.Unlock() }

func (m *ServerManager) stopPollerLocked() {
	if m.pollCancel != nil {
		m.pollCancel()
		m.pollCancel = nil
	}
}

func (m *ServerManager) stopWatchersLocked() {
	m.stopPollerLocked()
	m.stopTailLocked()
}

func (m *ServerManager) stopTailLocked() {
	if m.tailCancel != nil {
		m.tailGeneration++
		m.tailCancel()
		m.tailCancel = nil
	}
}

func (m *ServerManager) pollLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if !m.pollOnce(ctx) {
			return
		}
		if !waitContext(ctx, interval) {
			return
		}
	}
}

func (m *ServerManager) pollOnce(ctx context.Context) bool {
	state := m.Status().State
	if state != ServerRunning && state != ServerStopping && state != ServerStarting {
		return false
	}
	m.mu.Lock()
	poll := m.poll
	m.mu.Unlock()
	if poll == nil {
		return true
	}
	output, err := poll(ctx)
	if err == nil {
		m.noteShutdownWakeup()
		m.mu.Lock()
		observe := m.pollObserver
		m.mu.Unlock()
		if observe != nil {
			observe(output)
		}
	}
	n, ok := ParseOnlineCount(output)
	m.mu.Lock()
	canceled := ctx.Err() != nil
	if !canceled {
		if err == nil && ok {
			m.onlinePlayers = &n
		} else {
			m.onlinePlayers = nil
		}
		if err == nil {
			m.markReadyLocked() // Telnet answering means the server finished starting
		}
	}
	m.mu.Unlock()
	if canceled {
		return false
	}
	state = m.Status().State
	return state == ServerRunning || state == ServerStopping || state == ServerStarting
}

// noteShutdownWakeup records a successful in-flight game poll while shutdown is
// pending; it is recovery evidence if the process still survives the timeout.
func (m *ServerManager) noteShutdownWakeup() {
	m.mu.Lock()
	if m.shutdownActive && m.shutdownTarget != nil && !m.shutdownTarget.SentAt.IsZero() {
		m.shutdownWoke = true
	}
	m.mu.Unlock()
}

func (m *ServerManager) tailLoop(ctx context.Context, interval time.Duration, generation uint64) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	path := filepath.Join(m.paths.Logs, "server-current.log")
	var file *os.File
	var reader *bufio.Reader
	var offset int64
	var pending string
	var fingerprint []byte
	var previous os.FileInfo
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	for {
		if file == nil {
			var err error
			file, err = os.Open(path)
			if err != nil {
				file = nil
				if previous != nil {
					previous, offset, pending, fingerprint = nil, 0, "", nil
					if !m.setTailBuild(generation, "unknown") {
						return
					}
				}
				if !waitContext(ctx, interval) {
					return
				}
				continue
			}
			current, err := file.Stat()
			if err != nil {
				_ = file.Close()
				file = nil
				if !waitContext(ctx, interval) {
					return
				}
				continue
			}
			if previous != nil && (!os.SameFile(previous, current) || current.Size() < offset || !logTailMatches(file, offset, fingerprint)) {
				offset, pending, fingerprint = 0, "", nil
				if !m.setTailBuild(generation, "unknown") {
					return
				}
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				_ = file.Close()
				file = nil
				if !waitContext(ctx, interval) {
					return
				}
				continue
			}
			previous, reader = current, bufio.NewReader(file)
		}
		part, err := reader.ReadString('\n')
		offset += int64(len(part))
		fingerprint = append(fingerprint, part...)
		if len(fingerprint) > 128 {
			fingerprint = append([]byte(nil), fingerprint[len(fingerprint)-128:]...)
		}
		pending += part
		if strings.HasSuffix(pending, "\n") {
			if !m.publishLogLine(generation, strings.TrimSuffix(strings.TrimSuffix(pending, "\n"), "\r")) {
				return
			}
			pending = ""
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			_ = file.Close()
			file = nil
			if !waitContext(ctx, interval) {
				return
			}
			continue
		}
		_ = file.Close()
		file, reader = nil, nil
		if !waitContext(ctx, interval) {
			return
		}
	}
}

func logTailMatches(file *os.File, offset int64, fingerprint []byte) bool {
	if offset == 0 || len(fingerprint) == 0 {
		return true
	}
	buf := make([]byte, len(fingerprint))
	if _, err := file.ReadAt(buf, offset-int64(len(buf))); err != nil {
		return false
	}
	return bytes.Equal(buf, fingerprint)
}

func waitContext(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *ServerManager) publishLogLine(generation uint64, line string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.tailGeneration || m.tailCancel == nil {
		return false
	}
	if build, ok := parseServerBuild(line); ok {
		if m.build == "unknown" {
			m.build = build
		}
	}
	if strings.Contains(line, serverReadyMarker) {
		m.markReadyLocked()
	}
	if m.events != nil {
		m.events.Publish(Event{Type: "log", Source: "server", Message: line})
	}
	return true
}

func (m *ServerManager) setTailBuild(generation uint64, build string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.tailGeneration || m.tailCancel == nil {
		return false
	}
	m.build = build
	return true
}

func (m *ServerManager) Shutdown(ctx context.Context, telnet TelnetClient) error {
	target, err := m.beginShutdown()
	if err != nil {
		return err
	}
	if err := telnet.Shutdown(ctx); err != nil {
		m.cancelShutdown(target.Operation)
		return err
	}
	m.mu.Lock()
	if m.shutdownActive && target.Operation == m.shutdownOperation && m.shutdownTarget != nil {
		m.exitExpected = true
	}
	m.mu.Unlock()
	timeout := m.shutdownTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	if !m.waitForTargetExit(target.Process, timeout) {
		m.mu.Lock()
		failed := m.shutdownActive && target.Operation == m.shutdownOperation && m.shutdownWoke
		m.mu.Unlock()
		if failed {
			m.finishShutdown(target.Operation, ServerShutdownFailed, "正常关闭未完成")
		} else {
			m.finishShutdown(target.Operation, ServerStopTimeout, "")
		}
		return ErrShutdownTimeout
	}
	m.mu.Lock()
	m.clearTargetRecordLocked(target.Process)
	m.mu.Unlock()
	m.finishShutdown(target.Operation, ServerStopped, "")
	return nil
}

func (m *ServerManager) beginShutdown() (shutdownTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdownActive {
		return shutdownTarget{}, ErrShutdownInProgress
	}
	if m.refreshLocked().State != ServerRunning {
		return shutdownTarget{}, ErrServerStopped
	}
	process, err := loadProcess(m.paths)
	if err != nil {
		return shutdownTarget{}, err
	}
	identity, err := m.probe.Lookup(process.PID)
	if err != nil || !sameProcess(process, identity) {
		return shutdownTarget{}, ErrServerStopped
	}
	m.shutdownActive = true
	m.forceToken, m.forceTarget, m.exitExpected, m.shutdownWoke = "", persistedProcess{}, false, false
	m.shutdownOperation++
	m.state = ServerStopping
	m.onlinePlayers = nil
	m.stopPollerLocked()
	target := shutdownTarget{Operation: m.shutdownOperation, Process: process, SentAt: time.Now()}
	m.shutdownTarget = &target
	go m.publishState(ServerStopping)
	return target, nil
}

func (m *ServerManager) cancelShutdown(operation uint64) {
	m.mu.Lock()
	if !m.shutdownActive || operation != m.shutdownOperation {
		m.mu.Unlock()
		return
	}
	m.shutdownActive, m.shutdownTarget, m.shutdownWoke, m.exitExpected = false, nil, false, false
	m.state, m.diagnostic = ServerRunning, ""
	m.mu.Unlock()
	m.publishState(ServerRunning)
	m.StartPoller()
}

func (m *ServerManager) finishShutdown(operation uint64, wanted ServerState, diagnostics ...string) {
	diagnostic := ""
	if len(diagnostics) > 0 {
		diagnostic = diagnostics[0]
	}
	m.mu.Lock()
	if !m.shutdownActive || operation != m.shutdownOperation {
		m.mu.Unlock()
		return
	}
	previous := m.state
	if previous == ServerStopping {
		m.state = wanted
	}
	m.shutdownActive = false
	m.shutdownWoke = false
	if wanted == ServerStopped {
		m.exitExpected = false
		m.onlinePlayers = nil
		m.shutdownTarget, m.forceTarget = nil, persistedProcess{}
	} else {
		m.diagnostic = diagnostic
		m.issueForceTokenLocked()
	}
	state := m.state
	m.mu.Unlock()
	if state != previous {
		m.publishState(state)
	}
	if state == ServerRunning {
		m.StartPoller()
	}
}

func (m *ServerManager) publishState(state ServerState) {
	if m.events != nil {
		m.events.Publish(Event{Type: "server", Data: map[string]string{"state": string(state)}})
	}
}

func (m *ServerManager) waitForTargetExit(process persistedProcess, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		identity, err := m.probe.Lookup(process.PID)
		if errors.Is(err, os.ErrProcessDone) || (err == nil && !sameProcess(process, identity)) {
			return true
		}
		time.Sleep(m.pollIntervalOrDefault())
	}
	identity, err := m.probe.Lookup(process.PID)
	return errors.Is(err, os.ErrProcessDone) || (err == nil && !sameProcess(process, identity))
}

func (m *ServerManager) pollIntervalOrDefault() time.Duration {
	if m.processPollInterval > 0 {
		return m.processPollInterval
	}
	return 100 * time.Millisecond
}

func (m *ServerManager) SetOnlinePlayers(n int) { m.mu.Lock(); m.onlinePlayers = &n; m.mu.Unlock() }

func (m *ServerManager) Status() ServerStatus {
	m.mu.Lock()
	status := m.refreshLocked()
	status.Build = m.build
	if !m.lastCrash.IsZero() {
		crashed := m.lastCrash
		status.LastCrash = &crashed
	}
	m.mu.Unlock()
	status.World, status.GameName = m.infoCache.get(m.paths)
	return status
}

// serverInfoCache avoids re-parsing serverconfig.xml on every status request;
// it re-reads only when the file's size or modification time changes.
type serverInfoCache struct {
	mu              sync.Mutex
	size            int64
	modified        time.Time
	world, gameName string
}

func (c *serverInfoCache) get(paths Paths) (string, string) {
	info, err := os.Stat(paths.ServerConfig)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.size, c.modified = -1, time.Time{}
		return "unknown", "unknown"
	}
	if c.world == "" || info.Size() != c.size || !info.ModTime().Equal(c.modified) {
		c.world, c.gameName = serverInfo(paths)
		c.size, c.modified = info.Size(), info.ModTime()
	}
	return c.world, c.gameName
}

func (m *ServerManager) RequireRunning() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked().State != ServerRunning {
		return ErrServerStopped
	}
	return nil
}

func serverInfo(paths Paths) (world, gameName string) {
	world, gameName = "unknown", "unknown"
	if b, err := os.ReadFile(paths.ServerConfig); err == nil {
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			token, err := d.Token()
			if err != nil {
				break
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "property" {
				continue
			}
			switch attrValue(start.Attr, "name") {
			case "GameWorld":
				if value := attrValue(start.Attr, "value"); value != "" {
					world = value
				}
			case "GameName":
				if value := attrValue(start.Attr, "value"); value != "" {
					gameName = value
				}
			}
		}
	}
	return
}

func scanServerBuild(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if build, ok := parseServerBuild(line); ok {
			return build
		}
		if err != nil {
			return "unknown"
		}
	}
}

func parseServerBuild(line string) (string, bool) {
	index := strings.Index(line, "INF Version:")
	if index < 0 {
		return "", false
	}
	build := strings.TrimSpace(line[index+len("INF Version:"):])
	build = strings.TrimSpace(strings.SplitN(build, "Compatibility Version:", 2)[0])
	if match := officialServerBuild.FindStringSubmatch(build); match != nil {
		return "V" + match[1] + "-b" + match[2], true
	}
	return build, build != ""
}

var officialServerBuild = regexp.MustCompile(`^V[ \t]+([0-9]+(?:\.[0-9]+)+)[ \t]+\(b([0-9]+)\)$`)

func (m *ServerManager) Start() error {
	m.mu.Lock()
	if m.maintenanceActive {
		m.mu.Unlock()
		return ErrServerMaintenance
	}
	if m.shutdownActive {
		m.mu.Unlock()
		return ErrShutdownInProgress
	}
	status := m.refreshLocked()
	if status.State != ServerStopped && (status.State != ServerCrashed || m.launched != nil) {
		m.mu.Unlock()
		return ErrServerRunning
	}
	if m.start == nil {
		m.mu.Unlock()
		return errors.New("server starter is unavailable")
	}
	if m.startCheck != nil {
		if err := m.startCheck(); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.state = ServerStarting
	m.forceToken, m.build, m.exitExpected, m.onlinePlayers, m.lastCrash = "", "unknown", false, nil, time.Time{}
	launched, err := m.start(m.paths)
	if err != nil {
		m.state = ServerStopped
		m.mu.Unlock()
		return err
	}
	if launched == nil {
		m.state = ServerStopped
		m.mu.Unlock()
		return errors.New("server starter returned no process")
	}
	identity, err := m.probe.Lookup(launched.PID())
	if err != nil || !strings.EqualFold(filepath.Clean(identity.Exe), filepath.Clean(m.paths.ServerExe)) {
		m.retainLaunchedLocked(launched)
		m.mu.Unlock()
		if err != nil {
			return m.killLaunched(launched, err)
		}
		return m.killLaunched(launched, errors.New("started process executable does not match server executable"))
	}
	if err := saveProcess(m.paths, persistedProcess{PID: identity.PID, Exe: identity.Exe, Started: identity.Started}); err != nil {
		m.retainLaunchedLocked(launched)
		m.mu.Unlock()
		return m.killLaunched(launched, err)
	}
	state := ServerRunning
	if m.requireReady {
		state, m.startingSince = ServerStarting, time.Now()
	}
	m.state = state
	err = launched.Release()
	m.mu.Unlock()
	if err == nil {
		m.publishState(state)
		m.StartPoller()
	}
	return err
}

func (m *ServerManager) markReadyLocked() {
	if m.state == ServerStarting {
		m.state = ServerRunning
		m.publishState(ServerRunning)
	}
}

func (m *ServerManager) BeginConfigMutation() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.refreshLocked()
	if m.maintenanceActive {
		return nil, ErrServerBusy
	}
	m.configMutations++
	released := false
	return func() {
		m.mu.Lock()
		if !released {
			released = true
			m.configMutations--
		}
		m.mu.Unlock()
	}, nil
}

func (m *ServerManager) BeginStoppedOperation() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.maintenanceActive {
		return nil, ErrServerMaintenance
	}
	if m.configMutations > 0 {
		return nil, ErrServerBusy
	}
	if m.refreshLocked().State != ServerStopped {
		return nil, ErrServerRunning
	}
	m.maintenanceActive = true
	m.maintenanceID++
	id := m.maintenanceID
	return func() {
		m.mu.Lock()
		if m.maintenanceActive && m.maintenanceID == id {
			m.maintenanceActive = false
		}
		m.mu.Unlock()
	}, nil
}

func (m *ServerManager) retainLaunchedLocked(process launchedProcess) {
	m.exitExpected = false
	m.launched, m.state, m.forceToken = process, ServerCrashed, newForceToken()
}

func (m *ServerManager) killLaunched(process launchedProcess, cause error) error {
	if err := process.Kill(); err != nil {
		m.mu.Lock()
		if m.launched == process {
			m.state = ServerCrashed
			if m.forceToken == "" {
				m.forceToken = newForceToken()
			}
		}
		m.mu.Unlock()
		return errors.Join(cause, err)
	}
	m.mu.Lock()
	if m.launched == process {
		m.state, m.forceToken = ServerStopping, ""
	}
	m.mu.Unlock()
	go m.waitLaunched(process)
	return cause
}

func (m *ServerManager) waitLaunched(process launchedProcess) {
	_ = process.Wait()
	m.mu.Lock()
	if m.launched != process {
		m.mu.Unlock()
		return
	}
	m.launched, m.state, m.forceToken, m.diagnostic, m.exitExpected = nil, ServerStopped, "", "", false
	m.mu.Unlock()
	m.publishState(ServerStopped)
}

func (m *ServerManager) RequestForceStop(confirm string) error {
	m.mu.Lock()
	status := m.refreshLocked()
	if m.launched != nil {
		if m.forceStopActive || !forceStopState(status.State) || confirm == "" || confirm != m.forceToken {
			m.mu.Unlock()
			return ErrForceStopConfirmation
		}
		launched := m.launched
		m.forceToken, m.state, m.forceStopActive = "", ServerStopping, true
		m.stopWatchersLocked()
		m.mu.Unlock()
		m.publishState(ServerStopping)
		if err := launched.Kill(); err != nil {
			m.mu.Lock()
			if m.launched == launched {
				m.state, m.forceToken, m.forceStopActive = ServerCrashed, newForceToken(), false
			}
			m.mu.Unlock()
			return err
		}
		_ = launched.Wait() // Wait returning proves exit; a killed process normally reports a non-zero status.
		m.mu.Lock()
		if m.launched == launched {
			m.launched, m.state, m.forceToken, m.forceStopActive, m.exitExpected = nil, ServerStopped, "", false, false
		}
		m.mu.Unlock()
		m.publishState(ServerStopped)
		return nil
	}
	if m.forceStopActive || !forceStopState(status.State) || status.PID == 0 {
		m.mu.Unlock()
		return ErrForceStopConfirmation
	}
	verified, ok := m.probe.(verifiedProcessProbe)
	if !ok {
		m.mu.Unlock()
		return ErrVerifiedTerminationRequired
	}
	if confirm == "" || confirm != m.forceToken {
		m.mu.Unlock()
		return ErrForceStopConfirmation
	}
	m.forceToken, m.forceStopActive = "", true // one confirmation can never be replayed
	process := m.forceTarget
	if process.PID == 0 {
		m.state, m.forceStopActive = ServerStopTimeout, false
		m.diagnostic = "force-stop target unavailable"
		m.mu.Unlock()
		return ErrForceStopConfirmation
	}
	identity, err := m.probe.Lookup(process.PID)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		m.state, m.forceToken, m.forceStopActive = ServerStopTimeout, "", false
		m.diagnostic = "process probe failed: " + err.Error()
		m.mu.Unlock()
		m.publishState(ServerStopTimeout)
		return err
	}
	if err != nil || !sameProcess(process, identity) {
		previous := m.state
		state := m.targetGoneLocked(process)
		m.mu.Unlock()
		if state != previous {
			m.publishState(state)
		}
		return ErrServerStopped
	}
	m.state = ServerStopping
	m.exitExpected = true
	m.stopWatchersLocked()
	m.mu.Unlock()
	err = verified.TerminateVerified(process)
	if err != nil {
		m.mu.Lock()
		if m.state == ServerStopping {
			m.state, m.forceToken, m.forceStopActive, m.exitExpected = ServerStopTimeout, newForceToken(), false, false
		}
		m.mu.Unlock()
		return err
	}
	exited, observationErr := m.waitForProcessExit(process, m.forceStopTimeout)
	if !exited {
		m.mu.Lock()
		previous := m.state
		if m.state != ServerStopped {
			m.state, m.forceToken, m.forceStopActive = ServerStopTimeout, newForceToken(), false
			if observationErr != nil {
				m.diagnostic = "process probe failed: " + observationErr.Error()
			}
		}
		state := m.state
		m.mu.Unlock()
		if state != previous {
			m.publishState(state)
		}
		return ErrShutdownTimeout
	}
	m.mu.Lock()
	previous := m.state
	m.clearTargetRecordLocked(process)
	m.state, m.forceToken, m.diagnostic, m.forceStopActive, m.exitExpected = ServerStopped, "", "", false, false
	m.stopWatchersLocked()
	m.shutdownActive = false
	m.shutdownOperation++
	m.shutdownTarget, m.forceTarget = nil, persistedProcess{}
	m.mu.Unlock()
	if previous != ServerStopped {
		m.publishState(ServerStopped)
	}
	return nil
}

func forceStopState(state ServerState) bool {
	return state == ServerStarting || state == ServerRunning || state == ServerStopping || state == ServerStopTimeout || state == ServerShutdownFailed || state == ServerCrashed
}

func (m *ServerManager) waitForProcessExit(process persistedProcess, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	interval := m.pollIntervalOrDefault()
	deadline := time.Now().Add(timeout)
	var observationErr error
	for {
		identity, err := m.probe.Lookup(process.PID)
		if errors.Is(err, os.ErrProcessDone) || (err == nil && !sameProcess(process, identity)) {
			return true, nil
		}
		if err != nil {
			observationErr = err
		} else {
			observationErr = nil
		}
		if !time.Now().Before(deadline) {
			return false, observationErr
		}
		time.Sleep(interval)
	}
}

func (m *ServerManager) refreshLocked() ServerStatus {
	previous := m.state
	defer func() {
		if m.state != previous {
			m.publishState(m.state)
		}
	}()
	if m.launched != nil {
		if m.state != ServerRunning {
			m.stopWatchersLocked()
		}
		if !m.shutdownActive && !m.forceStopActive && m.forceToken == "" && forceStopState(m.state) {
			m.forceToken = newForceToken()
		}
		return ServerStatus{State: m.state, PID: m.launched.PID(), ForceStopToken: m.forceToken, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
	}
	if m.shutdownTarget != nil {
		target := m.shutdownTarget.Process
		identity, err := m.probe.Lookup(target.PID)
		if errors.Is(err, os.ErrProcessDone) || (err == nil && !sameProcess(target, identity)) {
			m.targetGoneLocked(target)
			if m.shutdownActive {
				return ServerStatus{State: m.state, OnlinePlayers: m.onlinePlayers}
			}
		} else if err != nil {
			m.diagnostic = "process probe failed: " + err.Error()
			m.stopWatchersLocked()
			return ServerStatus{State: m.state, PID: target.PID, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
		} else if m.state == ServerStopping || m.state == ServerStopTimeout || m.state == ServerShutdownFailed {
			m.diagnostic = shutdownDiagnostic(m.state, m.diagnostic)
			m.issueForceTokenLocked()
			return ServerStatus{State: m.state, PID: target.PID, ForceStopToken: m.forceToken, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
		}
	}
	process, err := loadProcess(m.paths)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !m.shutdownActive && !m.exitExpected && (m.state == ServerStopped || m.state == ServerCrashed) {
			if m.state == ServerCrashed {
				m.clearProcessLocked(ServerStopped)
			}
			m.diagnostic = ""
			return ServerStatus{State: m.state, OnlinePlayers: m.onlinePlayers}
		}
		if m.state == ServerStopped || m.state == ServerCrashed {
			m.state = ServerStopTimeout
		}
		m.forceToken = ""
		m.diagnostic = "process record unavailable: " + err.Error()
		m.stopWatchersLocked()
		return ServerStatus{State: m.state, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
	}
	identity, err := m.probe.Lookup(process.PID)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		if m.state == ServerStopped || m.state == ServerCrashed {
			m.state, m.forceToken = ServerStopTimeout, ""
		}
		m.diagnostic = "process probe failed: " + err.Error()
		m.stopWatchersLocked()
		return ServerStatus{State: m.state, PID: process.PID, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
	}
	if err != nil {
		state := m.processGoneLocked()
		return ServerStatus{State: state, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers}
	}
	if !sameProcess(process, identity) {
		m.clearProcessLocked(ServerStopped)
		return ServerStatus{State: ServerStopped, OnlinePlayers: m.onlinePlayers}
	}
	m.diagnostic = ""
	switch m.state {
	case ServerStopping, ServerStopTimeout, ServerShutdownFailed, ServerCrashed:
	case ServerStarting:
		if m.startupGrace > 0 && time.Since(m.startingSince) > m.startupGrace {
			m.state = ServerRunning // no readiness signal; don't keep the controls locked forever
		}
	default:
		m.state = ServerRunning
	}
	m.issueForceTokenLockedFor(process)
	started := process.Started
	return ServerStatus{State: m.state, PID: process.PID, ForceStopToken: m.forceToken, Diagnostic: m.diagnostic, OnlinePlayers: m.onlinePlayers, StartedAt: &started}
}

func shutdownDiagnostic(state ServerState, diagnostic string) string {
	if state == ServerShutdownFailed && diagnostic == "" {
		return "正常关闭未完成"
	}
	return diagnostic
}

func (m *ServerManager) issueForceTokenLocked() {
	if m.shutdownTarget != nil {
		m.issueForceTokenLockedFor(m.shutdownTarget.Process)
	}
}

func (m *ServerManager) issueForceTokenLockedFor(process persistedProcess) {
	if m.shutdownActive || m.forceStopActive || m.forceToken != "" || !forceStopState(m.state) {
		return
	}
	if _, ok := m.probe.(verifiedProcessProbe); ok {
		m.forceToken, m.forceTarget = newForceToken(), process
	}
}

func (m *ServerManager) processGoneLocked() ServerState {
	state := ServerCrashed
	if m.shutdownActive || m.exitExpected {
		state = ServerStopped
	} else {
		m.lastCrash = time.Now()
	}
	m.clearProcessLocked(state)
	return state
}

func (m *ServerManager) targetGoneLocked(target persistedProcess) ServerState {
	m.clearTargetRecordLocked(target)
	m.shutdownTarget, m.forceTarget, m.forceToken, m.forceStopActive, m.exitExpected = nil, persistedProcess{}, "", false, false
	m.state, m.diagnostic, m.onlinePlayers = ServerStopped, "", nil
	m.stopWatchersLocked()
	return m.state
}

func (m *ServerManager) clearTargetRecordLocked(target persistedProcess) {
	process, err := loadProcess(m.paths)
	if err == nil && process.PID == target.PID && sameProcess(process, ProcessIdentity{PID: target.PID, Exe: target.Exe, Started: target.Started}) {
		_ = os.Remove(processPath(m.paths))
	}
}

func (m *ServerManager) clearProcessLocked(state ServerState) {
	_ = os.Remove(processPath(m.paths))
	m.state, m.forceToken, m.forceTarget, m.diagnostic, m.forceStopActive, m.exitExpected, m.onlinePlayers = state, "", persistedProcess{}, "", false, false, nil
	m.stopWatchersLocked()
}

func processPath(paths Paths) string { return filepath.Join(paths.Logs, "server-process.json") }

func loadProcess(paths Paths) (persistedProcess, error) {
	b, err := os.ReadFile(processPath(paths))
	if err != nil {
		return persistedProcess{}, err
	}
	var process persistedProcess
	if err := json.Unmarshal(b, &process); err != nil {
		return persistedProcess{}, err
	}
	if process.PID <= 0 || process.Exe == "" || process.Started.IsZero() {
		return persistedProcess{}, errors.New("invalid persisted process")
	}
	return process, nil
}

func saveProcess(paths Paths, process persistedProcess) error {
	if err := os.MkdirAll(paths.Logs, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(process)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(paths.Logs, "server-process-*.json")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(b); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(name, processPath(paths))
}

func sameProcess(saved persistedProcess, actual ProcessIdentity) bool {
	return saved.PID == actual.PID && strings.EqualFold(filepath.Clean(saved.Exe), filepath.Clean(actual.Exe)) && saved.Started.Equal(actual.Started)
}

func newForceToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func startServer(paths Paths) (launchedProcess, error) {
	if err := os.MkdirAll(paths.Logs, 0700); err != nil {
		return nil, err
	}
	// Best effort: if rotation fails the game simply overwrites the old log.
	_ = rotateServerLog(paths.Logs, keptServerLogs)
	cmd := exec.Command(paths.ServerExe, serverArgs(paths)...)
	cmd.Dir = paths.ServerDir
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return osLaunchedProcess{cmd.Process}, nil
}

const keptServerLogs = 5

// rotateServerLog keeps the previous run's log, which the game would
// otherwise overwrite, as server-<modified>.log and prunes older copies.
func rotateServerLog(logs string, keep int) error {
	current := filepath.Join(logs, "server-current.log")
	info, err := os.Stat(current)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	stamp := info.ModTime().Format("20060102-150405")
	target := filepath.Join(logs, "server-"+stamp+".log")
	for n := 1; exists(target); n++ {
		target = filepath.Join(logs, fmt.Sprintf("server-%s-%d.log", stamp, n))
	}
	if err := os.Rename(current, target); err != nil {
		return err
	}
	previous, err := filepath.Glob(filepath.Join(logs, "server-[0-9]*.log"))
	if err != nil {
		return err
	}
	sort.Strings(previous)
	for len(previous) > keep {
		_ = os.Remove(previous[0])
		previous = previous[1:]
	}
	return nil
}

func serverArgs(paths Paths) []string {
	return []string{
		"-quit", "-batchmode", "-nographics", "-dedicated",
		"-configfile=" + paths.ServerConfig,
		"-logfile", filepath.Join(paths.Logs, "server-current.log"),
	}
}

type osLaunchedProcess struct{ process *os.Process }

func (p osLaunchedProcess) PID() int       { return p.process.Pid }
func (p osLaunchedProcess) Kill() error    { return p.process.Kill() }
func (p osLaunchedProcess) Wait() error    { _, err := p.process.Wait(); return err }
func (p osLaunchedProcess) Release() error { return p.process.Release() }
