//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procProcessProbe identifies processes through /proc. It exists so the panel
// builds and tests on non-Windows hosts; production releases target Windows.
type procProcessProbe struct{}

func newProcessProbe() ProcessProbe { return procProcessProbe{} }

func (procProcessProbe) Lookup(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return ProcessIdentity{}, os.ErrProcessDone
	}
	if err != nil {
		return ProcessIdentity{}, err
	}
	// Fields after the parenthesised command name: state is field 3,
	// starttime field 22.
	end := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat[end+1:]))
	if end < 0 || len(fields) < 20 {
		return ProcessIdentity{}, fmt.Errorf("unexpected /proc/%d/stat format", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" { // exited, not yet reaped
		return ProcessIdentity{}, os.ErrProcessDone
	}
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil {
		return ProcessIdentity{}, err
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{PID: pid, Exe: filepath.Clean(exe), Started: bootTime().Add(time.Duration(ticks) * time.Second / clockTicks)}, nil
}

// clockTicks is USER_HZ, which Linux fixes at 100 for user-visible values.
const clockTicks = 100

func bootTime() time.Time {
	data, err := os.ReadFile("/proc/stat")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "btime "); ok {
				if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
					return time.Unix(seconds, 0)
				}
			}
		}
	}
	return time.Unix(1, 0)
}
