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

func TestBackupRejectsRunningServerBeforeCreatingFile(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	started := time.Unix(10, 0)
	server := newServerManager(paths, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: paths.ServerExe, Started: started}}, nil)
	writeRecordedProcess(t, paths, persistedProcess{PID: 1, Exe: paths.ServerExe, Started: started})
	_, err := (BackupService{paths: paths, server: server}).CreateUserData(context.Background())
	if !errors.Is(err, ErrServerMustBeStopped) {
		t.Fatalf("CreateUserData() error = %v", err)
	}
	entries, globErr := filepath.Glob(filepath.Join(paths.UserDataBackups, "*"))
	if globErr != nil || len(entries) != 0 {
		t.Fatalf("backup files = %v, %v", entries, globErr)
	}
}

func TestBackupCreatesSafeUserdataZip(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	for name, data := range map[string]string{
		"Saves/Navezgane/game.txt": "save",
		"serveradmin.xml":          "<admin/>",
		"empty/nested/.keep":       "",
		"Saves/save..old":          "safe",
	} {
		path := filepath.Join(paths.UserData, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := (BackupService{paths: paths, server: NewServerManager(paths, nil)}).CreateUserData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(filepath.Join(paths.UserDataBackups, info.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	wants := map[string]bool{"Saves/Navezgane/game.txt": false, "serveradmin.xml": false, "empty/nested/.keep": false, "Saves/save..old": false}
	for _, file := range archive.File {
		if strings.Contains(file.Name, "\\") || strings.HasPrefix(file.Name, "/") {
			t.Fatalf("unsafe ZIP name %q", file.Name)
		}
		for _, part := range strings.Split(file.Name, "/") {
			if part == ".." {
				t.Fatalf("unsafe ZIP name %q", file.Name)
			}
		}
		if _, ok := wants[file.Name]; ok {
			wants[file.Name] = true
		}
	}
	for name, found := range wants {
		if !found {
			t.Errorf("missing %q", name)
		}
	}
}

func TestBackupCancellationRemovesTemporaryFile(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	path := filepath.Join(paths.UserData, "Saves", "active.dat")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	starts := 0
	server := newServerManager(paths, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 2, Exe: paths.ServerExe, Started: time.Unix(1, 0)}}, func(Paths) (launchedProcess, error) {
		starts++
		return &fakeLaunchedProcess{pid: 2}, nil
	})
	_, err := (BackupService{paths: paths, server: server}).CreateUserData(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateUserData() error = %v", err)
	}
	tmp, err := filepath.Glob(filepath.Join(paths.UserDataBackups, "*.tmp"))
	if err != nil || len(tmp) != 0 {
		t.Fatalf("temporary files = %v, %v", tmp, err)
	}
	if err := server.Start(); err != nil || starts != 1 {
		t.Fatalf("lease not released: Start()=%v, calls=%d", err, starts)
	}
}

func TestCopyWithContextCancelsBlockedPipeRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	written := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- copyWithContext(ctx, pipeWriter(written), read) }()
	if _, err := write.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	<-written
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("copy error = %v", err)
		}
	case <-time.After(time.Second):
		read.Close()
		t.Fatal("copy did not return after cancellation")
	}
}

type pipeWriter chan struct{}

func (w pipeWriter) Write(p []byte) (int, error) { w <- struct{}{}; return len(p), nil }

func TestUniqueBackupNameNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if got := uniqueBackupName(dir, "userdata-20260101-000000"); got != "userdata-20260101-000000.zip" {
		t.Fatalf("first name = %q", got)
	}
	for _, name := range []string{"userdata-20260101-000000.zip", "userdata-20260101-000000-2.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := uniqueBackupName(dir, "userdata-20260101-000000"); got != "userdata-20260101-000000-3.zip" {
		t.Fatalf("collision name = %q", got)
	}
}
