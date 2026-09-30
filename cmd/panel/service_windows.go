//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
)

func runAsService(run func(context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	return true, svc.Run(serviceName, panelService{preflight: checkServicePathsFromExecutable, run: run})
}

type panelService struct {
	preflight func() error
	run       func(context.Context) error
}

func (service panelService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- serviceStatus(serviceStartPending)
	if service.preflight != nil {
		if err := service.preflight(); err != nil {
			fmt.Fprintln(os.Stderr, "service path check failed:", err)
			changes <- serviceStatus(serviceStopped)
			return false, 1
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.run(ctx) }()
	changes <- serviceStatus(serviceRunning)
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- serviceStatus(serviceRunning)
			case svc.Stop, svc.Shutdown:
				changes <- serviceStatus(serviceStopPending)
				cancel()
				select {
				case err := <-done:
					changes <- serviceStatus(serviceStopped)
					return false, serviceExitCode(err)
				case <-time.After(20 * time.Second):
					changes <- serviceStatus(serviceStopped)
					return false, 1
				}
			}
		case err := <-done:
			changes <- serviceStatus(serviceStopped)
			return false, serviceExitCode(err)
		}
	}
}

func serviceStatus(transition serviceTransition) svc.Status {
	status := svc.Status{State: [...]svc.State{svc.StartPending, svc.Running, svc.StopPending, svc.Stopped}[transition]}
	controls := serviceAcceptedControls(transition)
	if controls&serviceAcceptStop != 0 {
		status.Accepts |= svc.AcceptStop
	}
	if controls&serviceAcceptShutdown != 0 {
		status.Accepts |= svc.AcceptShutdown
	}
	return status
}

func serviceExitCode(err error) uint32 {
	if err != nil {
		return 1
	}
	return 0
}
