//go:build windows

package main

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestPanelServiceEmitsStopControlsOnlyWhileRunning(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	done := make(chan struct{})
	var exitCode uint32
	go func() {
		_, exitCode = (panelService{run: func(ctx context.Context) error { <-ctx.Done(); return nil }}).Execute(nil, requests, changes)
		close(done)
	}()
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	<-done
	if exitCode != 0 {
		t.Fatalf("exit=%d", exitCode)
	}
	want := []svc.Status{
		{State: svc.StartPending},
		{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown},
		{State: svc.StopPending},
		{State: svc.Stopped},
	}
	for index, expected := range want {
		if got := <-changes; got.State != expected.State || got.Accepts != expected.Accepts {
			t.Fatalf("status %d=%+v want %+v", index, got, expected)
		}
	}
}

func TestPanelServiceStopsBeforeRunningWhenPreflightFails(t *testing.T) {
	changes := make(chan svc.Status, 2)
	run := false
	_, exitCode := (panelService{
		preflight: func() error { return errors.New("logs are not writable") },
		run:       func(context.Context) error { run = true; return nil },
	}).Execute(nil, make(chan svc.ChangeRequest), changes)
	if run || exitCode == 0 {
		t.Fatalf("run=%v exit=%d", run, exitCode)
	}
	want := []svc.Status{{State: svc.StartPending}, {State: svc.Stopped}}
	for index, expected := range want {
		if got := <-changes; got.State != expected.State || got.Accepts != 0 {
			t.Fatalf("status %d=%+v want %+v", index, got, expected)
		}
	}
}

func TestPanelServicePreflightPrecedesRunningAndRun(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		(panelService{
			preflight: func() error { close(entered); <-release; return nil },
			run: func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			},
		}).Execute(nil, requests, changes)
		close(done)
	}()
	if status := <-changes; status.State != svc.StartPending {
		t.Fatalf("first status=%+v", status)
	}
	<-entered
	if len(changes) != 0 {
		t.Fatalf("status emitted before preflight: %+v", <-changes)
	}
	close(release)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	<-done
	if status := <-changes; status.State != svc.Running {
		t.Fatalf("running status=%+v", status)
	}
}
