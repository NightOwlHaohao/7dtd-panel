package main

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeUserData(t *testing.T, paths Paths, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(paths.UserData, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeBackupZip(t *testing.T, paths Paths, name string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(paths.UserDataBackups, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(paths.UserDataBackups, name))
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for entry, data := range files {
		out, err := w.Create(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func runningBackupService(t *testing.T) BackupService {
	t.Helper()
	paths := ResolvePaths(t.TempDir())
	started := time.Unix(10, 0)
	server := newServerManager(paths, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: paths.ServerExe, Started: started}}, nil)
	writeRecordedProcess(t, paths, persistedProcess{PID: 1, Exe: paths.ServerExe, Started: started})
	return BackupService{paths: paths, server: server}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRestoreReplacesUserdataAndKeepsThePreviousCopy(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	service := BackupService{paths: paths, server: NewServerManager(paths, nil)}
	writeUserData(t, paths, map[string]string{"Saves/world/game.txt": "old", "only-old.txt": "x"})
	writeBackupZip(t, paths, "userdata-20260101-000000.zip", map[string]string{"Saves/world/game.txt": "restored", "serveradmin.xml": "<adminTools/>"})

	result, err := service.Restore(context.Background(), "userdata-20260101-000000.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, filepath.Join(paths.UserData, "Saves", "world", "game.txt")); got != "restored" {
		t.Fatalf("restored save = %q", got)
	}
	if exists(filepath.Join(paths.UserData, "only-old.txt")) {
		t.Fatal("restore must replace userdata, not merge into it")
	}
	if result.Previous == "" || readFileString(t, filepath.Join(result.Previous, "Saves", "world", "game.txt")) != "old" {
		t.Fatalf("previous userdata not kept: %+v", result)
	}
	if matches, _ := filepath.Glob(filepath.Join(paths.Root, ".userdata-restore-*")); len(matches) != 0 {
		t.Fatalf("stage left behind: %v", matches)
	}
}

func TestRestoreKeepsOnlyRecentSnapshots(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"userdata-20260101-000000", "userdata-20260102-000000", "userdata-20260103-000000", "userdata-20260104-000000"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	pruneRestoreSnapshots(dir, 3)
	if exists(filepath.Join(dir, "userdata-20260101-000000")) || !exists(filepath.Join(dir, "userdata-20260104-000000")) {
		t.Fatal("the oldest snapshot should be removed first")
	}
}

func TestRestoreRejectsUnsafeOrEmptyBackupsWithoutTouchingUserdata(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	service := BackupService{paths: paths, server: NewServerManager(paths, nil)}
	writeUserData(t, paths, map[string]string{"keep.txt": "current"})
	writeBackupZip(t, paths, "userdata-evil.zip", map[string]string{"../escape.txt": "x"})
	writeBackupZip(t, paths, "userdata-empty.zip", map[string]string{})
	for _, name := range []string{"userdata-evil.zip", "userdata-empty.zip"} {
		if _, err := service.Restore(context.Background(), name); !errors.Is(err, ErrInvalidBackup) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	for _, name := range []string{"../userdata-x.zip", "other.zip", `userdata-a\b.zip`, ""} {
		if _, err := service.Restore(context.Background(), name); !errors.Is(err, ErrInvalidBackup) {
			t.Fatalf("name %q: error = %v", name, err)
		}
	}
	if _, err := service.Restore(context.Background(), "userdata-missing.zip"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("missing backup error = %v", err)
	}
	if got := readFileString(t, filepath.Join(paths.UserData, "keep.txt")); got != "current" || exists(filepath.Join(paths.Root, "escape.txt")) {
		t.Fatalf("userdata changed by a rejected restore: %q", got)
	}
}

func TestRestoreRequiresStoppedServer(t *testing.T) {
	service := runningBackupService(t)
	writeUserData(t, service.paths, map[string]string{"keep.txt": "current"})
	writeBackupZip(t, service.paths, "userdata-1.zip", map[string]string{"keep.txt": "old"})
	if _, err := service.Restore(context.Background(), "userdata-1.zip"); !errors.Is(err, ErrServerMustBeStopped) {
		t.Fatalf("error = %v", err)
	}
	if got := readFileString(t, filepath.Join(service.paths.UserData, "keep.txt")); got != "current" {
		t.Fatalf("userdata changed while the server runs: %q", got)
	}
}

func TestLiveBackupSavesTheWorldFirst(t *testing.T) {
	previous := liveBackupSettle
	liveBackupSettle = time.Millisecond
	t.Cleanup(func() { liveBackupSettle = previous })
	service := runningBackupService(t)
	writeUserData(t, service.paths, map[string]string{"Saves/world/region.7rg": "data"})
	if _, err := service.Create(context.Background(), false, nil); !errors.Is(err, ErrServerMustBeStopped) {
		t.Fatalf("without saveworld a running server must refuse: %v", err)
	}
	saved := 0
	info, err := service.Create(context.Background(), true, func(context.Context) error { saved++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if saved != 1 || !info.Live || !info.Auto || !strings.HasPrefix(info.Name, autoBackupPrefix) {
		t.Fatalf("saved=%d info=%+v", saved, info)
	}
	if _, err := service.Create(context.Background(), false, func(context.Context) error { return errors.New("telnet down") }); err == nil || !strings.Contains(err.Error(), "saveworld") {
		t.Fatalf("a failed saveworld must stop the backup: %v", err)
	}
	// While a live backup holds its lease, stopped-only work is refused.
	release, err := service.server.BeginConfigMutation()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestPruneAutoKeepsManualBackups(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	service := BackupService{paths: paths}
	for _, name := range []string{"userdata-20260101-000000.zip", "userdata-auto-20260101-000000.zip", "userdata-auto-20260102-000000.zip", "userdata-auto-20260103-000000.zip"} {
		writeBackupZip(t, paths, name, map[string]string{"a": "b"})
	}
	removed, err := service.PruneAuto(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "userdata-auto-20260101-000000.zip" {
		t.Fatalf("removed = %v", removed)
	}
	if !exists(filepath.Join(paths.UserDataBackups, "userdata-20260101-000000.zip")) {
		t.Fatal("a manual backup was pruned")
	}
	if err := service.Delete("userdata-20260101-000000.zip"); err != nil || exists(filepath.Join(paths.UserDataBackups, "userdata-20260101-000000.zip")) {
		t.Fatalf("delete: %v", err)
	}
}

func TestBackupRestoreAndDeleteRoutes(t *testing.T) {
	a := newTestApp(t)
	writeUserData(t, a.paths, map[string]string{"keep.txt": "current"})
	writeBackupZip(t, a.paths, "userdata-1.zip", map[string]string{"keep.txt": "old"})
	if rr := serveJSON(t, a, "POST", "/api/backups/userdata-1.zip/restore", `{}`); rr.Code != 400 {
		t.Fatalf("restore without confirmation = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/backups/userdata-2.zip/restore", `{"confirm":true}`); rr.Code != 404 || apiCode(t, rr) != "backup_restore_failed" {
		t.Fatalf("restore missing = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/backups/userdata-1.zip/restore", `{"confirm":true}`); rr.Code != 200 {
		t.Fatalf("restore = %d %s", rr.Code, rr.Body)
	}
	if got := readFileString(t, filepath.Join(a.paths.UserData, "keep.txt")); got != "old" {
		t.Fatalf("restored = %q", got)
	}
	if rr := serveJSON(t, a, "DELETE", "/api/backups/userdata-1.zip", ""); rr.Code != 200 || exists(filepath.Join(a.paths.UserDataBackups, "userdata-1.zip")) {
		t.Fatalf("delete = %d %s", rr.Code, rr.Body)
	}
}
