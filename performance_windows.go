//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes          = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx    = modKernel32.NewProc("GlobalMemoryStatusEx")
	procK32GetProcessMemoryInfo = modKernel32.NewProc("K32GetProcessMemoryInfo")

	modPdh                          = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery                = modPdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter        = modPdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = modPdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modPdh.NewProc("PdhGetFormattedCounterValue")
	procPdhGetFormattedCounterArray = modPdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery               = modPdh.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble   = 0x00000200
	pdhFmtNoCap100 = 0x00008000
	pdhMoreData    = 0x800007D2

	pdhCounterCPU = `\Processor Information(_Total)\% Processor Utility`
	pdhCounterGPU = `\GPU Engine(*)\Utilization Percentage`

	// PDH rates are computed between the last two collections; a shorter
	// interval than this gives noise rather than a meaningful percentage.
	pdhMinInterval = 250 * time.Millisecond
)

type pdhFmtCounterValue struct {
	CStatus uint32
	_       uint32
	Double  float64
}

type pdhFmtCounterValueItem struct {
	Name  *uint16
	Value pdhFmtCounterValue
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// windowsPerformanceSource reads host counters through PDH (the API behind
// Task Manager and perfmon) and process counters through kernel32.
type windowsPerformanceSource struct {
	query       uintptr // 0 when PDH is unavailable
	cpu, gpu    uintptr // 0 when that counter does not exist on this host
	lastCollect time.Time
}

func openPerformanceSource() (performanceSource, error) {
	for _, proc := range []*windows.LazyProc{procGetSystemTimes, procGlobalMemoryStatusEx, procK32GetProcessMemoryInfo} {
		if err := proc.Find(); err != nil {
			return nil, err
		}
	}
	source := &windowsPerformanceSource{}
	if modPdh.Load() != nil || pdhStatus(procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&source.query)))) != nil {
		source.query = 0
		return source, nil // CPU times, memory and the game process still work without PDH
	}
	source.cpu = source.addCounter(pdhCounterCPU)
	source.gpu = source.addCounter(pdhCounterGPU)
	source.collect()
	return source, nil
}

func (s *windowsPerformanceSource) addCounter(path string) uintptr {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var counter uintptr
	if pdhStatus(procPdhAddEnglishCounter.Call(s.query, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&counter)))) != nil {
		return 0
	}
	return counter
}

// collect refreshes PDH and reports whether the new rates span a usable interval.
func (s *windowsPerformanceSource) collect() bool {
	if s.query == 0 {
		return false
	}
	now := time.Now()
	usable := !s.lastCollect.IsZero() && now.Sub(s.lastCollect) >= pdhMinInterval
	if pdhStatus(procPdhCollectQueryData.Call(s.query)) != nil {
		return false
	}
	s.lastCollect = now
	return usable
}

func (s *windowsPerformanceSource) Read(gamePID int) (performanceReading, error) {
	var reading performanceReading
	if s.collect() {
		if value, ok := pdhValue(s.cpu); ok {
			reading.CPUUtility, reading.HasCPUUtility = value, true
		}
		if instances, ok := pdhInstances(s.gpu); ok {
			reading.GPU, reading.HasGPU = gpuUtilization(instances)
		}
	}

	var idle, kernel, user windows.Filetime
	if ok, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		reading.CPUIdle = filetimeTicks(idle)
		reading.CPUTotal = filetimeTicks(kernel) + filetimeTicks(user) // kernel time includes idle time
		reading.HasCPUTimes = true
	}

	memory := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memory))); ok != 0 {
		reading.MemoryTotal, reading.MemoryAvailable, reading.HasMemory = memory.TotalPhys, memory.AvailPhys, true
	}

	if gamePID > 0 {
		readGameProcess(gamePID, &reading)
	}
	if !reading.HasCPUTimes && !reading.HasMemory {
		return reading, errors.New("kernel32 performance calls failed")
	}
	return reading, nil
}

func readGameProcess(pid int, reading *performanceReading) {
	reading.GamePID = pid
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) == nil {
		reading.GameCPU, reading.HasGameCPU = filetimeTicks(kernel)+filetimeTicks(user), true
	}
	counters := processMemoryCounters{CB: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	if ok, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB)); ok != 0 {
		reading.GameWorkingSet, reading.HasGameMemory = uint64(counters.WorkingSetSize), true
	}
}

func (s *windowsPerformanceSource) Close() error {
	if s.query == 0 {
		return nil
	}
	err := pdhStatus(procPdhCloseQuery.Call(s.query))
	s.query, s.cpu, s.gpu = 0, 0, 0
	return err
}

func pdhValue(counter uintptr) (float64, bool) {
	if counter == 0 {
		return 0, false
	}
	var kind uint32
	var value pdhFmtCounterValue
	if pdhStatus(procPdhGetFormattedCounterValue.Call(counter, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(&value)))) != nil || !pdhValid(value.CStatus) {
		return 0, false
	}
	return value.Double, true
}

// pdhInstances returns every valid instance of a wildcard counter by name.
func pdhInstances(counter uintptr) (map[string]float64, bool) {
	if counter == 0 {
		return nil, false
	}
	var size, count uint32
	status, _, _ := procPdhGetFormattedCounterArray.Call(counter, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if status != pdhMoreData || size == 0 {
		return map[string]float64{}, status == 0
	}
	// The buffer holds the item array followed by the instance name strings.
	itemSize := uint32(unsafe.Sizeof(pdhFmtCounterValueItem{}))
	buffer := make([]pdhFmtCounterValueItem, size/itemSize+1)
	if pdhStatus(procPdhGetFormattedCounterArray.Call(counter, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buffer[0])))) != nil {
		return nil, false
	}
	instances := make(map[string]float64, count)
	for _, item := range buffer[:min(int(count), len(buffer))] {
		if item.Name != nil && pdhValid(item.Value.CStatus) {
			instances[windows.UTF16PtrToString(item.Name)] = item.Value.Double
		}
	}
	return instances, true
}

func pdhValid(status uint32) bool { return status == 0 || status == 1 } // VALID_DATA, NEW_DATA

func pdhStatus(status, _ uintptr, _ error) error {
	if status != 0 {
		return fmt.Errorf("PDH status 0x%08X", uint32(status))
	}
	return nil
}

func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}
