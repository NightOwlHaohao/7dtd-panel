package main

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const steamCMDURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip"

type UpdateTarget string

const (
	UpdateServer   UpdateTarget = "server"
	UpdateSteamCMD UpdateTarget = "steamcmd"
)

type UpdateStatus struct {
	State     string       `json:"state"`
	Target    UpdateTarget `json:"target,omitempty"`
	LastError string       `json:"lastError,omitempty"`
}

type Updater struct {
	paths       Paths
	server      *ServerManager
	events      *EventBroker
	client      *http.Client
	downloadURL string
	run         func(context.Context, string, []string, func(string)) error
	rename      func(string, string) error
	mu          sync.Mutex
	status      UpdateStatus
	current     *updateRun
	audit       func(string) error
	steam       func() SteamSettings // branch to install; nil means the default
	checking    bool                 // a version check is using SteamCMD
}

type updateRun struct {
	done   chan struct{}
	err    error
	target UpdateTarget
}

const updateCanceledMessage = "SteamCMD update canceled"

func NewUpdater(paths Paths, server *ServerManager, events *EventBroker) *Updater {
	return &Updater{paths: paths, server: server, events: events, client: &http.Client{Timeout: 5 * time.Minute}, downloadURL: steamCMDURL, run: runSteamCMD, status: UpdateStatus{State: "idle"}}
}

func (u *Updater) Status() UpdateStatus { u.mu.Lock(); defer u.mu.Unlock(); return u.status }

func (u *Updater) Run(ctx context.Context, target UpdateTarget) error {
	if !validUpdateTarget(target) {
		return errors.New("invalid update target")
	}
	if err := u.auditWrite("update target=" + string(target) + " start attempt"); err != nil {
		return fmt.Errorf("%w: %s", ErrAuditUnavailable, sanitizedError(err))
	}
	active, release, err := u.claim(ctx, target)
	if err != nil {
		return err
	}
	return u.runClaimed(ctx, active, release)
}

func (u *Updater) Start(ctx context.Context, target UpdateTarget) error {
	if !validUpdateTarget(target) {
		return errors.New("invalid update target")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := u.auditWrite("update target=" + string(target) + " start attempt"); err != nil {
		return fmt.Errorf("%w: %s", ErrAuditUnavailable, sanitizedError(err))
	}
	active, release, err := u.claim(ctx, target)
	if err != nil {
		return err
	}
	go func() { _ = u.runClaimed(ctx, active, release) }()
	return nil
}

func validUpdateTarget(target UpdateTarget) bool {
	return target == UpdateServer || target == UpdateSteamCMD
}

func (u *Updater) claim(ctx context.Context, target UpdateTarget) (*updateRun, func(), error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if u.current != nil {
		select {
		case <-u.current.done:
		default:
			return nil, nil, ErrServerMaintenance
		}
	}
	if u.checking {
		return nil, nil, ErrServerBusy
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if u.server == nil {
		return nil, nil, errors.New("server manager is unavailable")
	}
	release, err := u.server.BeginStoppedOperation()
	if err != nil {
		return nil, nil, err
	}
	active := &updateRun{done: make(chan struct{}), target: target}
	u.current = active
	u.status = UpdateStatus{State: "running", Target: target}
	return active, release, nil
}

func (u *Updater) runClaimed(ctx context.Context, active *updateRun, release func()) (err error) {
	defer func() {
		u.finish(active, release, err, ctx.Err())
		close(active.done)
	}()
	if u.server.Status().State != ServerStopped {
		return ErrServerRunning
	}
	args := []string{"+quit"}
	if active.target == UpdateServer {
		config, backupErr := u.backup()
		if backupErr != nil {
			return fmt.Errorf("backup before SteamCMD: %w", backupErr)
		}
		defer func() {
			if restoreErr := os.WriteFile(u.paths.ServerConfig, config, 0600); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore serverconfig.xml after SteamCMD: %w", restoreErr))
			}
		}()
		var steam SteamSettings
		if u.steam != nil {
			steam = u.steam()
		}
		args = appUpdateArgs(u.paths.ServerDir, steam)
	}
	if !exists(u.paths.SteamCMD) {
		if err := u.install(ctx); err != nil {
			return fmt.Errorf("SteamCMD download/install: %w", err)
		}
	}
	run := u.run
	if run == nil {
		run = runSteamCMD
	}
	err = run(ctx, u.paths.SteamCMD, args, u.publish)
	if err != nil {
		return fmt.Errorf("SteamCMD execution: %w", err)
	}
	return nil
}

// GameBranches asks SteamCMD for the game's branches and their latest
// builds. It never runs alongside an update, which uses SteamCMD too.
func (u *Updater) GameBranches(ctx context.Context) ([]RemoteBranch, error) {
	u.mu.Lock()
	if u.current != nil {
		select {
		case <-u.current.done:
		default:
			u.mu.Unlock()
			return nil, ErrServerMaintenance
		}
	}
	if u.checking {
		u.mu.Unlock()
		return nil, ErrServerBusy
	}
	u.checking = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = false
		u.mu.Unlock()
	}()
	if !exists(u.paths.SteamCMD) {
		return nil, errors.New("SteamCMD is not installed")
	}
	run := u.run
	if run == nil {
		run = runSteamCMD
	}
	var output strings.Builder
	var outputMu sync.Mutex
	err := run(ctx, u.paths.SteamCMD, []string{"+login", "anonymous", "+app_info_update", "1", "+app_info_print", gameAppID, "+quit"}, func(line string) {
		outputMu.Lock()
		output.WriteString(line)
		output.WriteByte('\n')
		outputMu.Unlock()
	})
	if err != nil {
		return nil, fmt.Errorf("SteamCMD app_info_print: %w", err)
	}
	outputMu.Lock()
	defer outputMu.Unlock()
	return parseAppInfoBranches(output.String())
}

func (u *Updater) Wait() error { return u.WaitContext(context.Background()) }

func (u *Updater) WaitContext(ctx context.Context) error {
	u.mu.Lock()
	active := u.current
	u.mu.Unlock()
	if active == nil {
		return nil
	}
	select {
	case <-active.done:
		u.mu.Lock()
		err := active.err
		u.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *Updater) finish(active *updateRun, release func(), runErr, ctxErr error) {
	status := UpdateStatus{State: "success", Target: active.target}
	message := "SteamCMD update target=" + string(active.target) + " complete"
	if ctxErr != nil {
		reason := sanitizedError(runErr)
		if reason == "" {
			reason = sanitizedError(ctxErr)
		}
		status = UpdateStatus{State: "canceled", Target: active.target, LastError: reason}
		message = updateCanceledMessage + " target=" + string(active.target) + ": " + reason
	} else if runErr != nil {
		reason := sanitizedError(runErr)
		status = UpdateStatus{State: "error", Target: active.target, LastError: reason}
		message = "SteamCMD update target=" + string(active.target) + " failed: " + reason
	}
	auditMessage := "update target=" + string(active.target) + " result=" + status.State
	if status.LastError != "" {
		auditMessage += " reason=" + status.LastError
	}
	if auditErr := u.auditWrite(auditMessage); auditErr != nil {
		err := fmt.Errorf("%w: %v", ErrAuditUnavailable, auditErr)
		runErr = errors.Join(runErr, err)
		reason := "audit result failed: " + sanitizedError(auditErr)
		if status.LastError != "" {
			status.LastError += "; " + reason
		} else {
			status.LastError = reason
		}
		status.State = "error"
		message = "SteamCMD update target=" + string(active.target) + " failed: " + status.LastError
	}
	u.mu.Lock()
	active.err = runErr
	u.status = status
	release()
	u.mu.Unlock()
	if u.events != nil {
		u.events.Publish(Event{Type: "update", Source: "steamcmd", Message: message})
	}
}

func (u *Updater) install(ctx context.Context) error {
	client := u.client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	url := u.downloadURL
	if url == "" {
		url = steamCMDURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" && url == steamCMDURL {
		return errors.New("SteamCMD download must use HTTPS")
	}
	if err := os.MkdirAll(u.paths.SteamCMDDir, 0700); err != nil {
		return err
	}
	tmp := filepath.Join(u.paths.SteamCMDDir, "steamcmd.zip.tmp")
	defer os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("SteamCMD download returned HTTP %d", resp.StatusCode)
		} else {
			_, err = io.Copy(out, resp.Body)
		}
		resp.Body.Close()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	archive, err := zip.OpenReader(tmp)
	if err != nil {
		return err
	}
	defer archive.Close()
	stage, err := os.MkdirTemp(filepath.Dir(u.paths.SteamCMDDir), filepath.Base(u.paths.SteamCMDDir)+".new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, file := range archive.File {
		target := filepath.Join(stage, filepath.FromSlash(file.Name))
		rel, err := filepath.Rel(stage, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return errors.New("unsafe SteamCMD ZIP path")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if !file.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		closeOut, closeIn := out.Close(), in.Close()
		if err != nil {
			return err
		}
		if closeOut != nil {
			return closeOut
		}
		if closeIn != nil {
			return closeIn
		}
	}
	if !exists(filepath.Join(stage, filepath.Base(u.paths.SteamCMD))) {
		return errors.New("SteamCMD ZIP does not contain steamcmd.exe")
	}
	if err := archive.Close(); err != nil {
		return err
	}
	rename := u.rename
	if rename == nil {
		rename = os.Rename
	}
	old := u.paths.SteamCMDDir + ".old-" + fmt.Sprint(time.Now().UnixNano())
	if err := rename(u.paths.SteamCMDDir, old); err != nil {
		return err
	}
	if err := rename(stage, u.paths.SteamCMDDir); err != nil {
		if rollbackErr := rename(old, u.paths.SteamCMDDir); rollbackErr != nil {
			oldPath, absErr := filepath.Abs(old)
			if absErr != nil {
				oldPath = old
			}
			return fmt.Errorf("SteamCMD swap failed: %w; rollback failed: %v; existing SteamCMD is recoverable at %s", err, rollbackErr, oldPath)
		}
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

func (u *Updater) auditWrite(message string) error {
	if u.audit == nil {
		return nil
	}
	return u.audit(message)
}

func (u *Updater) backup() ([]byte, error) {
	stamp := time.Now().Format("20060102-150405.000000000")
	if err := os.MkdirAll(u.paths.ConfigBackups, 0700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(u.paths.ServerConfig)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(u.paths.ConfigBackups, stamp+"-config.xml"), b, 0600); err != nil {
		return nil, err
	}
	var mods []string
	err = filepath.WalkDir(filepath.Join(u.paths.ServerDir, "Mods"), func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			rel, err := filepath.Rel(filepath.Join(u.paths.ServerDir, "Mods"), path)
			if err != nil {
				return err
			}
			mods = append(mods, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(mods)
	contents := strings.Join(mods, "\n")
	if contents != "" {
		contents += "\n"
	}
	if err := os.WriteFile(filepath.Join(u.paths.ConfigBackups, stamp+"-mods.txt"), []byte(contents), 0600); err != nil {
		return nil, err
	}
	return b, nil
}

func (u *Updater) publish(line string) {
	if u.events != nil && line != "" {
		u.events.Publish(Event{Type: "console", Source: "steamcmd", Message: line})
	}
}

func runSteamCMD(ctx context.Context, exe string, args []string, publish func(string)) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs <- publishSteamCMDLines(stdout, publish) }()
	go func() { defer wg.Done(); errs <- publishSteamCMDLines(stderr, publish) }()
	wg.Wait()
	waitErr := cmd.Wait()
	err = errors.Join(waitErr, <-errs, <-errs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func publishSteamCMDLines(r io.Reader, publish func(string)) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			publish(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
