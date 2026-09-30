package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceTransitionsAreFixed(t *testing.T) {
	got := serviceTransitions()
	want := []serviceTransition{serviceStartPending, serviceRunning, serviceStopPending, serviceStopped}
	if len(got) != len(want) {
		t.Fatalf("transitions=%v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("transition %d=%v want %v", index, got[index], want[index])
		}
	}
}

func TestServiceRunningAcceptsOnlyStopAndShutdown(t *testing.T) {
	for _, transition := range serviceTransitions() {
		want := serviceControls(0)
		if transition == serviceRunning {
			want = serviceAcceptStop | serviceAcceptShutdown
		}
		if got := serviceAcceptedControls(transition); got != want {
			t.Fatalf("transition %v accepts=%v want %v", transition, got, want)
		}
	}
}

func TestRunAsServiceDoesNotHandleConsole(t *testing.T) {
	handled, err := runAsService(func(context.Context) error {
		t.Fatal("console run must not be called by runAsService")
		return nil
	})
	if runtime.GOOS != "windows" && (handled || err != nil) {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestCheckServicePathsIsReadOnly(t *testing.T) {
	paths := completeServicePaths(t)
	before, err := os.ReadFile(paths.PanelConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkServicePaths(paths); err != nil {
		t.Fatal(err)
	}
	assertNoServiceProbes(t, paths)
	after, err := os.ReadFile(paths.PanelConfig)
	if err != nil || string(after) != string(before) {
		t.Fatalf("panel config changed: %q %v", after, err)
	}
}

func TestCheckServicePathsRejectsInvalidLocations(t *testing.T) {
	paths := completeServicePaths(t)
	paths.ServerExe = paths.ServerDir // a folder where the program should be
	if err := checkServicePaths(paths); err == nil || !strings.Contains(err.Error(), "server executable") {
		t.Fatalf("wrong executable type error=%v", err)
	}
	assertNoServiceProbes(t, paths)
	paths = completeServicePaths(t)
	paths.Logs = filepath.Join(paths.Root, "missing-logs")
	if err := checkServicePaths(paths); err == nil || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("missing logs error=%v", err)
	}
	paths = completeServicePaths(t)
	paths.Root = "relative"
	if err := checkServicePaths(paths); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative root error=%v", err)
	}
}

func TestCheckServicePathsRejectsUnwritableFiles(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	paths := completeServicePaths(t)
	if err := os.Chmod(paths.ServerConfig, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(paths.ServerConfig, 0600) })
	if err := checkServicePaths(paths); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("unwritable config error=%v", err)
	}
	assertNoServiceProbes(t, paths)
}

func assertNoServiceProbes(t *testing.T, paths Paths) {
	t.Helper()
	for _, dir := range []string{paths.ServerDir, paths.UserData, paths.Backups, paths.Cache, paths.Logs} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".7dtd-panel-check-") {
				t.Fatalf("left probe %s in %s", entry.Name(), dir)
			}
		}
	}
}

func TestCheckServicePathsAllowsMissingGameFiles(t *testing.T) {
	paths := completeServicePaths(t)
	if err := os.RemoveAll(paths.ServerDir); err != nil {
		t.Fatal(err)
	}
	if err := checkServicePaths(paths); err != nil {
		t.Fatalf("a panel without the game installed yet must pass: %v", err)
	}
}

func TestPreparePanelFoldersCreatesConfigAndDataFolders(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if err := preparePanelFolders(paths); err != nil {
		t.Fatal(err)
	}
	if err := checkServicePaths(paths); err != nil {
		t.Fatalf("prepared folder is not ready for the service: %v", err)
	}
	if cfg, err := LoadPanelConfig(paths.PanelConfig); err != nil || cfg.Listen != "127.0.0.1:8787" {
		t.Fatalf("panel.json=%+v err=%v", cfg, err)
	}
}

func completeServicePaths(t *testing.T) Paths {
	t.Helper()
	paths := ResolvePaths(t.TempDir())
	for _, dir := range []string{paths.ServerDir, paths.UserData, paths.Backups, paths.Cache, paths.Logs} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{paths.PanelConfig: `{"listen":"127.0.0.1:8787"}`, paths.ServerExe: "", paths.ServerConfig: "<ServerSettings/>"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}
