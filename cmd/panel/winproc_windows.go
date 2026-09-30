package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const stillActive = 259 // STILL_ACTIVE exit code

type windowsProcessProbe struct{}

func newProcessProbe() ProcessProbe { return windowsProcessProbe{} }

func (windowsProcessProbe) Lookup(pid int) (ProcessIdentity, error) {
	handle, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION)
	if err != nil {
		return ProcessIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	return processIdentity(handle, pid)
}

// TerminateVerified kills the process only if it is still the recorded one,
// checked through the same handle that is used to terminate it.
func (windowsProcessProbe) TerminateVerified(process persistedProcess) error {
	handle, err := openProcess(process.PID, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	identity, err := processIdentity(handle, process.PID)
	if err != nil {
		return err
	}
	if !sameProcess(process, identity) {
		return errors.New("process identity changed before termination")
	}
	return windows.TerminateProcess(handle, 1)
}

func openProcess(pid int, access uint32) (windows.Handle, error) {
	if pid <= 0 {
		return 0, os.ErrProcessDone
	}
	handle, err := windows.OpenProcess(access, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // the PID no longer exists
		return 0, os.ErrProcessDone
	}
	return handle, err
}

func processIdentity(handle windows.Handle, pid int) (ProcessIdentity, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return ProcessIdentity{}, err
	}
	if code != stillActive {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return ProcessIdentity{}, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{PID: pid, Exe: filepath.Clean(syscall.UTF16ToString(buf[:size])), Started: time.Unix(0, created.Nanoseconds())}, nil
}
