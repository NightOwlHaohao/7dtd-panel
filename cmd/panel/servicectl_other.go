//go:build !windows

package main

import "context"

func newServiceControl(Paths, string, bool) serviceControl { return unsupportedServiceControl{} }

func runServiceCommand([]string, string, Paths) error { return errServiceUnsupported }

// launchInteractive runs the panel in the terminal: there is no service
// outside Windows.
func launchInteractive(run func(context.Context) error, _ string, _ PanelConfig) error {
	return runConsole(run)
}

// waitBeforeExit is only needed for the console window Windows opens on a
// double-click.
func waitBeforeExit() {}
