package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
)

// version is set at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "service" {
		if err := serviceCommand(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "service command failed:", err)
			os.Exit(1)
		}
		return
	}
	if handled, err := runAsService(func(ctx context.Context) error { return run(ctx, true) }); err != nil {
		fmt.Fprintln(os.Stderr, "panel service failed:", err)
		os.Exit(1)
	} else if handled {
		return
	}
	console := func(ctx context.Context) error { return run(ctx, false) }
	var err error
	if len(args) == 1 && args[0] == "--console" {
		err = runConsole(console)
	} else if len(args) == 0 {
		err = launchFromExecutable(console)
	} else {
		err = fmt.Errorf("unknown arguments %q; usage: panel.exe [--console | service install|uninstall|autostart on|off]", args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "panel startup failed:", err)
		waitBeforeExit()
		os.Exit(1)
	}
}

func serviceCommand(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runServiceCommand(args, exe, ResolvePaths(filepath.Dir(exe)))
}

// launchFromExecutable is the double-click start: on Windows it hands over
// to the installed service when there is one.
func launchFromExecutable(console func(context.Context) error) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	paths := ResolvePaths(filepath.Dir(exe))
	cfg, err := LoadPanelConfig(paths.PanelConfig)
	if err != nil {
		return err
	}
	return launchInteractive(console, exe, cfg)
}

type serviceTransition uint8
type serviceControls uint8

const (
	serviceStartPending serviceTransition = iota
	serviceRunning
	serviceStopPending
	serviceStopped
)

const (
	serviceAcceptStop serviceControls = 1 << iota
	serviceAcceptShutdown
)

func serviceTransitions() []serviceTransition {
	return []serviceTransition{serviceStartPending, serviceRunning, serviceStopPending, serviceStopped}
}

func serviceAcceptedControls(transition serviceTransition) serviceControls {
	if transition == serviceRunning {
		return serviceAcceptStop | serviceAcceptShutdown
	}
	return 0
}

func run(ctx context.Context, asService bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	paths := ResolvePaths(filepath.Dir(exe))
	cfg, err := LoadPanelConfig(paths.PanelConfig)
	if err != nil {
		return err
	}
	app, err := NewApp(paths, cfg)
	if err != nil {
		return err
	}
	app.service = newServiceControl(paths, exe, asService)
	fmt.Printf("7DTD panel %s — 面板地址: http://%s\n", version, cfg.Listen)
	if err := app.Serve(ctx); err != nil {
		return err
	}
	// For example starting the service after this console panel handed over.
	return app.lifecycle.runAfterStop()
}

func runConsole(run func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	go func() {
		for {
			<-interrupts
			cancel()
			return
		}
	}()
	return run(ctx)
}

func checkServicePathsFromExecutable() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(exe) {
		return fmt.Errorf("executable path is not absolute")
	}
	return checkServicePaths(ResolvePaths(filepath.Dir(exe)))
}

// checkServicePaths verifies that the folders the panel writes to exist and
// are writable. The game files are optional: the panel can download them.
func checkServicePaths(paths Paths) error {
	for _, check := range []struct {
		path, name string
		dir        bool
		writable   bool
		optional   bool
	}{
		{paths.Root, "panel directory", true, false, false},
		{paths.PanelConfig, "panel config", false, true, false},
		{paths.UserData, "userdata", true, true, false},
		{paths.Backups, "backups", true, true, false},
		{paths.Cache, "cache", true, true, false},
		{paths.Logs, "logs", true, true, false},
		{paths.ServerDir, "server directory", true, true, true},
		{paths.ServerExe, "server executable", false, false, true},
		{paths.ServerConfig, "server config", false, true, true},
	} {
		if !filepath.IsAbs(check.path) {
			return fmt.Errorf("%s path is not absolute", check.name)
		}
		info, err := os.Stat(check.path)
		if check.optional && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s is missing or unreadable: %w", check.name, err)
		}
		if info.IsDir() != check.dir {
			return fmt.Errorf("%s has the wrong type", check.name)
		}
		if check.writable {
			if err := checkServicePathWritable(check.path, check.dir); err != nil {
				return fmt.Errorf("%s is not writable: %w", check.name, err)
			}
		}
	}
	return nil
}

func checkServicePathWritable(path string, directory bool) error {
	if directory {
		return checkServiceDirectoryWritable(path)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

func checkServiceDirectoryWritable(path string) (err error) {
	probe, err := os.CreateTemp(path, ".7dtd-panel-check-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	defer func() {
		closeErr := probe.Close()
		removeErr := os.Remove(name)
		if closeErr != nil && removeErr != nil {
			err = fmt.Errorf("probe cleanup close: %v; remove: %w", closeErr, removeErr)
		} else if closeErr != nil {
			err = fmt.Errorf("probe cleanup close: %w", closeErr)
		} else if removeErr != nil {
			err = fmt.Errorf("probe cleanup remove: %w", removeErr)
		}
	}()
	return nil
}
