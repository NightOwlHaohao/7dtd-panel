package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func steamZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testUpdater(paths Paths, server *ServerManager, url string) *Updater {
	return &Updater{paths: paths, server: server, client: http.DefaultClient, downloadURL: url, status: UpdateStatus{State: "idle"}}
}

func asyncTestUpdater(t *testing.T, run func(context.Context, string, []string, func(string)) error) (*Updater, *EventBroker) {
	t.Helper()
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(steamZip(t, map[string]string{"steamcmd.exe": "new"}))
	}))
	t.Cleanup(h.Close)
	events := NewEventBroker()
	u := testUpdater(paths, NewServerManager(paths, events), h.URL)
	u.events, u.run = events, run
	return u, events
}

func waitUpdateState(t *testing.T, u *Updater, want string) UpdateStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if status := u.Status(); status.State == want {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("update state = %#v, want %q", u.Status(), want)
	return UpdateStatus{}
}

func TestUpdaterStartClaimsLeaseAndTransitionsToSuccess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	u, events := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		close(entered)
		<-release
		return nil
	})

	if err := u.Start(context.Background(), UpdateServer); err != nil {
		t.Fatal(err)
	}
	if got := u.Status().State; got != "running" {
		t.Fatalf("state after Start = %q", got)
	}
	if err := u.Run(context.Background(), UpdateSteamCMD); !errors.Is(err, ErrServerMaintenance) {
		t.Fatalf("concurrent Run = %v", err)
	}
	if err := u.server.Start(); !errors.Is(err, ErrServerMaintenance) {
		t.Fatalf("server Start during update = %v", err)
	}
	<-entered
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := u.WaitContext(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContext(active) = %v", err)
	}
	cancelWait()
	releaseOnce.Do(func() { close(release) })
	if err := u.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if got := waitUpdateState(t, u, "success"); got.LastError != "" {
		t.Fatalf("success status = %#v", got)
	}
	lease, err := u.server.BeginStoppedOperation()
	if err != nil {
		t.Fatalf("update lease not released: %v", err)
	}
	lease()
	u.run = func(context.Context, string, []string, func(string)) error { return nil }
	if err := u.Run(context.Background(), UpdateServer); err != nil {
		t.Fatalf("Run after completed Start = %v", err)
	}
	_, cancel, history := events.subscribe()
	defer cancel()
	if len(history) == 0 || history[len(history)-1].Type != "update" || !strings.Contains(history[len(history)-1].Message, "complete") {
		t.Fatalf("events = %#v", history)
	}
}

func TestUpdaterFailureStatusHidesInternalError(t *testing.T) {
	u, events := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		return errors.New(`C:\\secret\\steamcmd.exe failed with token=abc`)
	})
	if err := u.Start(context.Background(), UpdateServer); err != nil {
		t.Fatal(err)
	}
	status := waitUpdateState(t, u, "error")
	if status.LastError == "" || strings.Contains(status.LastError, "abc") || strings.Contains(status.LastError, `C:\\secret`) || !strings.Contains(status.LastError, "[REDACTED]") {
		t.Fatalf("unsafe status = %#v", status)
	}
	_ = u.Wait()
	_, cancel, history := events.subscribe()
	defer cancel()
	if len(history) == 0 || history[len(history)-1].Type != "update" || !strings.Contains(history[len(history)-1].Message, "failed") {
		t.Fatalf("events = %#v", history)
	}
}

func TestNewUpdaterUsesBoundedDownloadClient(t *testing.T) {
	u := NewUpdater(ResolvePaths(t.TempDir()), nil, nil)
	if u.client == nil || u.client.Timeout != 5*time.Minute {
		t.Fatalf("download client timeout = %v", u.client)
	}
}

func TestUpdaterDownloadTimeoutReleasesLeaseAndReportsError(t *testing.T) {
	u, _ := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		t.Fatal("SteamCMD runner reached after download timeout")
		return nil
	})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { <-req.Context().Done() }))
	defer slow.Close()
	u.downloadURL = slow.URL
	u.client = &http.Client{Timeout: 20 * time.Millisecond}
	if err := u.Run(context.Background(), UpdateServer); err == nil {
		t.Fatal("Run unexpectedly succeeded")
	}
	status := u.Status()
	if status.State != "error" || status.LastError == "" {
		t.Fatalf("status = %#v", status)
	}
	release, err := u.server.BeginConfigMutation()
	if err != nil {
		t.Fatalf("timeout retained maintenance lease: %v", err)
	}
	release()
}

func TestUpdaterAuditAttemptFailureDoesNotClaimOrRun(t *testing.T) {
	called := false
	u, _ := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		called = true
		return nil
	})
	u.audit = NewAuditLog(u.paths.Root).Write
	if err := u.Start(context.Background(), UpdateServer); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("Start = %v", err)
	}
	if called || u.Status().State != "idle" {
		t.Fatalf("called=%v status=%#v", called, u.Status())
	}
	release, err := u.server.BeginConfigMutation()
	if err != nil {
		t.Fatalf("audit failure claimed maintenance: %v", err)
	}
	release()
}

func TestUpdaterResultAuditFailureBecomesTerminalError(t *testing.T) {
	entered := make(chan struct{})
	releaseRun := make(chan struct{})
	u, events := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		close(entered)
		<-releaseRun
		return nil
	})
	auditPath := filepath.Join(u.paths.Logs, "panel.log")
	u.audit = NewAuditLog(auditPath).Write
	if err := u.Start(context.Background(), UpdateServer); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := os.Remove(auditPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(auditPath, 0700); err != nil {
		t.Fatal(err)
	}
	close(releaseRun)
	status := waitUpdateState(t, u, "error")
	if !strings.Contains(strings.ToLower(status.LastError), "audit") {
		t.Fatalf("status = %#v", status)
	}
	if err := u.Wait(); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("Wait = %v", err)
	}
	_, cancel, history := events.subscribe()
	defer cancel()
	if message := history[len(history)-1].Message; !strings.Contains(strings.ToLower(message), "audit") {
		t.Fatalf("event = %q", message)
	}
}

func TestUpdaterStartRejectsCanceledContext(t *testing.T) {
	called := make(chan struct{}, 1)
	u, _ := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		called <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := u.Start(ctx, UpdateServer); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(canceled) = %v", err)
	}
	select {
	case <-called:
		t.Fatal("canceled Start launched runner")
	case <-time.After(20 * time.Millisecond):
	}
	if got := u.Status().State; got != "idle" {
		t.Fatalf("status = %q", got)
	}
	release, err := u.server.BeginStoppedOperation()
	if err != nil {
		t.Fatalf("canceled Start retained lease: %v", err)
	}
	release()
}

func TestSteamCMDRejectsRunningServerBeforeHTTP(t *testing.T) {
	for _, target := range []UpdateTarget{UpdateServer, UpdateSteamCMD} {
		t.Run(string(target), func(t *testing.T) {
			paths := ResolvePaths(t.TempDir())
			called := false
			h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer h.Close()
			started := time.Unix(1, 0)
			server := newServerManager(paths, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: paths.ServerExe, Started: started}}, nil)
			writeRecordedProcess(t, paths, persistedProcess{PID: 1, Exe: paths.ServerExe, Started: started})
			err := testUpdater(paths, server, h.URL).Run(context.Background(), target)
			if !errors.Is(err, ErrServerRunning) || called {
				t.Fatalf("Run() = %v, HTTP called=%v", err, called)
			}
		})
	}
}

func TestSteamCMDNon200PreservesExistingInstall(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SteamCMD, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer h.Close()
	err := testUpdater(paths, NewServerManager(paths, nil), h.URL).Run(context.Background(), UpdateServer)
	got, readErr := os.ReadFile(paths.SteamCMD)
	if err == nil || readErr != nil || string(got) != "old" {
		t.Fatalf("Run=%v existing=%q read=%v", err, got, readErr)
	}
}

func TestUpdaterBacksUpBeforeDownloading(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SteamCMD, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	before := configHash([]byte("old"))
	called := false
	h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer h.Close()
	err := testUpdater(paths, NewServerManager(paths, nil), h.URL).Run(context.Background(), UpdateServer)
	got, readErr := os.ReadFile(paths.SteamCMD)
	if err == nil || called || readErr != nil || configHash(got) != before {
		t.Fatalf("Run=%v HTTP=%v existing=%q read=%v", err, called, got, readErr)
	}
}

func TestSteamCMDSwapReportsFailedRollback(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SteamCMD, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(steamZip(t, map[string]string{"steamcmd.exe": "new"}))
	}))
	defer h.Close()
	u := testUpdater(paths, NewServerManager(paths, nil), h.URL)
	var old string
	calls := 0
	u.rename = func(from, to string) error {
		calls++
		if calls == 1 {
			old = to
			return os.Rename(from, to)
		}
		if calls == 2 {
			return errors.New("stage rename failed")
		}
		return errors.New("rollback failed")
	}
	err := u.install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stage rename failed") || !strings.Contains(err.Error(), "rollback failed") || !strings.Contains(err.Error(), old) || !exists(old) {
		t.Fatalf("Run=%v old=%q exists=%v", err, old, exists(old))
	}
}

func TestSteamCMDRejectsZipTraversal(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SteamCMD, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(steamZip(t, map[string]string{"../evil.exe": "bad"}))
	}))
	defer h.Close()
	err := testUpdater(paths, NewServerManager(paths, nil), h.URL).Run(context.Background(), UpdateServer)
	got, _ := os.ReadFile(paths.SteamCMD)
	if err == nil || string(got) != "old" || exists(filepath.Join(paths.Root, "tools", "evil.exe")) {
		t.Fatalf("Run=%v existing=%q", err, got)
	}
}

func TestSteamCMDRejectsZipWithoutExecutable(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SteamCMD, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(steamZip(t, map[string]string{"readme.txt": "not SteamCMD"}))
	}))
	defer h.Close()
	err := testUpdater(paths, NewServerManager(paths, nil), h.URL).Run(context.Background(), UpdateServer)
	got, _ := os.ReadFile(paths.SteamCMD)
	if err == nil || string(got) != "old" {
		t.Fatalf("Run=%v existing=%q", err, got)
	}
}

func TestUpdaterTargetsUseOneLeaseAndStableArguments(t *testing.T) {
	for _, test := range []struct {
		target     UpdateTarget
		wantArgs   []string
		wantBackup bool
	}{
		{UpdateServer, []string{"+force_install_dir", "SERVER", "+login", "anonymous", "+app_update", "294420", "validate", "+quit"}, true},
		{UpdateSteamCMD, []string{"+quit"}, false},
	} {
		t.Run(string(test.target), func(t *testing.T) {
			paths := ResolvePaths(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(paths.ServerDir, "Mods", "z"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(paths.ServerDir, "Mods", "z", "b.txt"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(steamZip(t, map[string]string{"steamcmd.exe": "new"}))
			}))
			defer h.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			u := testUpdater(paths, NewServerManager(paths, nil), h.URL)
			var got []string
			u.run = func(_ context.Context, _ string, args []string, _ func(string)) error {
				got = append([]string(nil), args...)
				close(entered)
				<-release
				return nil
			}
			if err := u.Start(context.Background(), test.target); err != nil {
				t.Fatal(err)
			}
			if status := u.Status(); status.State != "running" || status.Target != test.target {
				t.Fatalf("running status = %#v", status)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("update did not reach runner")
			}
			other := UpdateServer
			if test.target == UpdateServer {
				other = UpdateSteamCMD
			}
			if err := u.Start(context.Background(), other); !errors.Is(err, ErrServerMaintenance) {
				t.Fatalf("concurrent Start = %v", err)
			}
			close(release)
			if err := u.Wait(); err != nil {
				t.Fatal(err)
			}
			if status := waitUpdateState(t, u, "success"); status.Target != test.target {
				t.Fatalf("success status = %#v", status)
			}
			want := append([]string(nil), test.wantArgs...)
			for i := range want {
				if want[i] == "SERVER" {
					want[i] = paths.ServerDir
				}
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("args = %#v, want %#v", got, want)
			}
			mods, err := filepath.Glob(filepath.Join(paths.ConfigBackups, "*-mods.txt"))
			if err != nil || (len(mods) == 1) != test.wantBackup {
				t.Fatalf("mods backups = %v, want backup=%v, err=%v", mods, test.wantBackup, err)
			}
		})
	}
}

func TestUpdaterUsesExistingSteamCMDWithoutDownload(t *testing.T) {
	for _, test := range []struct {
		target   UpdateTarget
		wantArgs []string
	}{
		{UpdateServer, []string{"+force_install_dir", "SERVER", "+login", "anonymous", "+app_update", "294420", "validate", "+quit"}},
		{UpdateSteamCMD, []string{"+quit"}},
	} {
		t.Run(string(test.target), func(t *testing.T) {
			paths := ResolvePaths(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ServerConfig, []byte("<ServerSettings/>"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.SteamCMD, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			downloaded := false
			h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { downloaded = true }))
			defer h.Close()
			u := testUpdater(paths, NewServerManager(paths, nil), h.URL)
			var exe string
			var got []string
			u.run = func(_ context.Context, actual string, args []string, _ func(string)) error {
				exe, got = actual, append([]string(nil), args...)
				return nil
			}

			if err := u.Run(context.Background(), test.target); err != nil {
				t.Fatal(err)
			}
			want := append([]string(nil), test.wantArgs...)
			for i := range want {
				if want[i] == "SERVER" {
					want[i] = paths.ServerDir
				}
			}
			if downloaded || exe != paths.SteamCMD || strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("downloaded=%v exe=%q args=%#v, want %q %#v", downloaded, exe, got, paths.SteamCMD, want)
			}
		})
	}
}

func TestServerUpdateRestoresConfigAfterSteamCMD(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("update failed")} {
		name := "success"
		if runErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			paths := ResolvePaths(t.TempDir())
			original := []byte(`<ServerSettings><property name="ServerName" value="mine" /></ServerSettings>`)
			if err := os.MkdirAll(filepath.Dir(paths.ServerConfig), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ServerConfig, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(paths.SteamCMDDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.SteamCMD, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			u := testUpdater(paths, NewServerManager(paths, nil), "")
			u.run = func(context.Context, string, []string, func(string)) error {
				if err := os.WriteFile(paths.ServerConfig, []byte("official default"), 0600); err != nil {
					t.Fatal(err)
				}
				return runErr
			}

			err := u.Run(context.Background(), UpdateServer)
			if runErr == nil && err != nil {
				t.Fatal(err)
			}
			if runErr != nil && err == nil {
				t.Fatal("failed SteamCMD update unexpectedly succeeded")
			}
			got, err := os.ReadFile(paths.ServerConfig)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("server config=%q, want original %q, read=%v", got, original, err)
			}
		})
	}
}

func TestUpdaterStatusRetainsTargetAcrossTerminalStates(t *testing.T) {
	for _, test := range []struct {
		name, wantState string
		target          UpdateTarget
		run             func(context.Context) error
	}{
		{"success", "success", UpdateServer, func(context.Context) error { return nil }},
		{"error", "error", UpdateSteamCMD, func(context.Context) error { return errors.New("runner failed") }},
		{"canceled", "canceled", UpdateServer, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, _ := asyncTestUpdater(t, func(ctx context.Context, _ string, _ []string, _ func(string)) error { return test.run(ctx) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := u.Start(ctx, test.target); err != nil {
				t.Fatal(err)
			}
			if status := u.Status(); status.State != "running" || status.Target != test.target {
				t.Fatalf("running status = %#v", status)
			}
			if test.wantState == "canceled" {
				cancel()
			}
			_ = u.Wait()
			if status := waitUpdateState(t, u, test.wantState); status.Target != test.target {
				t.Fatalf("%s status = %#v", test.wantState, status)
			}
		})
	}
}

func TestUpdaterRejectsInvalidTargetBeforeAuditOrLease(t *testing.T) {
	u, _ := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error { return nil })
	auditCalls := 0
	u.audit = func(string) error { auditCalls++; return nil }
	if err := u.Start(context.Background(), UpdateTarget("invalid")); err == nil {
		t.Fatal("Start accepted invalid target")
	}
	if auditCalls != 0 || u.Status().State != "idle" {
		t.Fatalf("invalid target started update: auditCalls=%d status=%#v", auditCalls, u.Status())
	}
	release, err := u.server.BeginStoppedOperation()
	if err != nil {
		t.Fatalf("invalid target acquired maintenance lease: %v", err)
	}
	release()
}

func TestRunSteamCMDDrainsLongOutputLines(t *testing.T) {
	if os.Getenv("STEAMCMD_HELPER") == "1" {
		fmt.Fprintln(os.Stdout, strings.Repeat("out", 30_000))
		fmt.Fprintln(os.Stderr, strings.Repeat("err", 30_000))
		return
	}
	t.Setenv("STEAMCMD_HELPER", "1")
	var lines []string
	var mu sync.Mutex
	if err := runSteamCMD(context.Background(), os.Args[0], []string{"-test.run=TestRunSteamCMDDrainsLongOutputLines"}, func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, line := range lines {
		total += len(line)
	}
	if len(lines) < 2 || total <= 64*1024 {
		t.Fatalf("published %d lines (%d bytes)", len(lines), total)
	}
}

func TestRunSteamCMDPublishesFinalUnterminatedOutput(t *testing.T) {
	if os.Getenv("STEAMCMD_HELPER") == "tail" {
		fmt.Fprint(os.Stdout, "final-stdout")
		fmt.Fprint(os.Stderr, "final-stderr")
		return
	}
	t.Setenv("STEAMCMD_HELPER", "tail")
	var lines []string
	var mu sync.Mutex
	if err := runSteamCMD(context.Background(), os.Args[0], []string{"-test.run=TestRunSteamCMDPublishesFinalUnterminatedOutput"}, func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "final-stdout") || !strings.Contains(got, "final-stderr") {
		t.Fatalf("missing final output: %q", got)
	}
}

func TestRunSteamCMDCancellationKillsAndWaits(t *testing.T) {
	if os.Getenv("STEAMCMD_HELPER") == "cancel" {
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(time.Minute)
		return
	}
	t.Setenv("STEAMCMD_HELPER", "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runSteamCMD(ctx, os.Args[0], []string{"-test.run=TestRunSteamCMDCancellationKillsAndWaits"}, func(line string) {
			if line == "ready" {
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("helper did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled SteamCMD = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled SteamCMD was not killed and waited")
	}
}
