package main

import (
	"context"
	"errors"
	"regexp"
	"runtime"
	"sync"
	"time"
)

type MetricValue struct {
	Available bool    `json:"available"`
	Value     float64 `json:"value"`
	Reason    string  `json:"reason,omitempty"`
}

type MemoryValue struct {
	Available  bool    `json:"available"`
	UsedBytes  float64 `json:"usedBytes"`
	TotalBytes float64 `json:"totalBytes"`
	Percent    float64 `json:"percent"`
	Reason     string  `json:"reason,omitempty"`
}

type PerformanceSample struct {
	Timestamp  time.Time   `json:"timestamp"`
	HostCPU    MetricValue `json:"hostCpu"`
	HostGPU    MetricValue `json:"hostGpu"`
	Memory     MemoryValue `json:"memory"`
	GameCPU    MetricValue `json:"gameCpu"`
	GameMemory MetricValue `json:"gameMemory"`
}

// performanceReading is one raw read of the platform counters. Rates that the
// OS already computes (PDH) arrive as percentages; cumulative times are in
// 100ns units and become rates by differencing two readings.
type performanceReading struct {
	CPUUtility    float64 // % Processor Utility, as Task Manager shows it
	HasCPUUtility bool
	CPUIdle       uint64 // GetSystemTimes fallback: idle and kernel+user time
	CPUTotal      uint64
	HasCPUTimes   bool

	MemoryTotal     uint64
	MemoryAvailable uint64
	HasMemory       bool

	GPU    float64 // busiest engine, summed over processes
	HasGPU bool

	GamePID        int
	GameCPU        uint64 // kernel+user time of the game process
	HasGameCPU     bool
	GameWorkingSet uint64
	HasGameMemory  bool
}

// performanceSource reads the counters; the Windows implementation uses
// PDH and kernel32 directly, so no helper process is involved.
type performanceSource interface {
	Read(gamePID int) (performanceReading, error)
	Close() error
}

// PerformanceMonitor samples on demand; Sample calls are serialized so
// that successive readings form well-defined intervals.
type PerformanceMonitor struct {
	server     *ServerManager
	open       func() (performanceSource, error)
	now        func() time.Time
	logicalCPU int

	mu         sync.Mutex
	source     performanceSource
	closed     bool
	previous   performanceReading
	previousAt time.Time
	havePrev   bool
}

func NewPerformanceMonitor(server *ServerManager) *PerformanceMonitor {
	return &PerformanceMonitor{server: server, open: openPerformanceSource, now: time.Now, logicalCPU: runtime.NumCPU()}
}

func (m *PerformanceMonitor) Sample(context.Context) (PerformanceSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return unavailableSample("performance monitoring is closed")
	}
	if m.source == nil {
		source, err := m.open()
		if err != nil {
			return unavailableSample("performance counters are unavailable: " + err.Error())
		}
		m.source = source
	}
	pid := 0
	if m.server != nil {
		pid = m.server.Status().PID
	}
	current, err := m.source.Read(pid)
	if err != nil {
		return unavailableSample("performance counters could not be read: " + err.Error())
	}
	now := m.now()
	var elapsed time.Duration
	if m.havePrev {
		elapsed = now.Sub(m.previousAt)
	}
	sample := calculatePerformance(m.previous, current, m.havePrev, elapsed, m.logicalCPU)
	sample.Timestamp = now
	m.previous, m.previousAt, m.havePrev = current, now, true
	if !sample.HostCPU.Available && !sample.HostGPU.Available && !sample.Memory.Available && !sample.GameCPU.Available && !sample.GameMemory.Available {
		return sample, errors.New("no performance metrics are available")
	}
	return sample, nil
}

func (m *PerformanceMonitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.source == nil {
		return nil
	}
	err := m.source.Close()
	m.source = nil
	return err
}

func unavailableSample(reason string) (PerformanceSample, error) {
	return PerformanceSample{
		Timestamp:  time.Now(),
		HostCPU:    unavailableMetric(reason),
		HostGPU:    unavailableMetric(reason),
		Memory:     MemoryValue{Reason: reason},
		GameCPU:    unavailableMetric(reason),
		GameMemory: unavailableMetric(reason),
	}, errors.New(reason)
}

func calculatePerformance(previous, current performanceReading, havePrevious bool, elapsed time.Duration, logicalCPU int) PerformanceSample {
	return PerformanceSample{
		Timestamp:  time.Now(),
		HostCPU:    calculateHostCPU(previous, current, havePrevious),
		HostGPU:    calculateHostGPU(current),
		Memory:     calculateMemory(current),
		GameCPU:    calculateGameCPU(previous, current, havePrevious, elapsed, logicalCPU),
		GameMemory: calculateGameMemory(current),
	}
}

func calculateHostCPU(previous, current performanceReading, havePrevious bool) MetricValue {
	if current.HasCPUUtility {
		return MetricValue{Available: true, Value: clampPercent(current.CPUUtility)}
	}
	if !havePrevious || !previous.HasCPUTimes || !current.HasCPUTimes || current.CPUTotal <= previous.CPUTotal || current.CPUIdle < previous.CPUIdle {
		return unavailableMetric("CPU metrics are unavailable")
	}
	total := float64(current.CPUTotal - previous.CPUTotal)
	idle := float64(current.CPUIdle - previous.CPUIdle)
	return MetricValue{Available: true, Value: clampPercent((total - idle) / total * 100)}
}

func calculateHostGPU(current performanceReading) MetricValue {
	if !current.HasGPU {
		return unavailableMetric("GPU metrics are unavailable")
	}
	return MetricValue{Available: true, Value: clampPercent(current.GPU)}
}

func calculateMemory(current performanceReading) MemoryValue {
	if !current.HasMemory || current.MemoryTotal == 0 || current.MemoryAvailable > current.MemoryTotal {
		return MemoryValue{Reason: "memory metrics are unavailable"}
	}
	total := float64(current.MemoryTotal)
	used := total - float64(current.MemoryAvailable)
	return MemoryValue{Available: true, UsedBytes: used, TotalBytes: total, Percent: clampPercent(used / total * 100)}
}

func calculateGameCPU(previous, current performanceReading, havePrevious bool, elapsed time.Duration, logicalCPU int) MetricValue {
	if !havePrevious || current.GamePID <= 0 || previous.GamePID != current.GamePID || !previous.HasGameCPU || !current.HasGameCPU || elapsed <= 0 || logicalCPU <= 0 {
		return unavailableMetric("game CPU metrics are unavailable")
	}
	if current.GameCPU < previous.GameCPU {
		return unavailableMetric("game CPU counter reset")
	}
	busy := time.Duration(current.GameCPU-previous.GameCPU) * 100 // 100ns units
	return MetricValue{Available: true, Value: clampPercent(busy.Seconds() / elapsed.Seconds() / float64(logicalCPU) * 100)}
}

func calculateGameMemory(current performanceReading) MetricValue {
	if current.GamePID <= 0 || !current.HasGameMemory {
		return unavailableMetric("game memory metrics are unavailable")
	}
	return MetricValue{Available: true, Value: float64(current.GameWorkingSet)}
}

// gpuEngineInstance matches "pid_<pid>_luid_..._engtype_<type>"; the part
// from "luid_" on identifies one engine of one adapter.
var gpuEngineInstance = regexp.MustCompile(`^pid_\d+_(luid_.+)$`)

// gpuUtilization mirrors Task Manager: sum each engine's utilization over
// all processes, then report the busiest engine.
func gpuUtilization(instances map[string]float64) (float64, bool) {
	engines := map[string]float64{}
	for name, value := range instances {
		match := gpuEngineInstance.FindStringSubmatch(name)
		if match == nil || value < 0 {
			continue
		}
		engines[match[1]] += value
	}
	if len(engines) == 0 {
		return 0, false
	}
	busiest := 0.0
	for _, value := range engines {
		busiest = max(busiest, value)
	}
	return clampPercent(busiest), true
}

func unavailableMetric(reason string) MetricValue { return MetricValue{Reason: reason} }

func clampPercent(value float64) float64 { return min(100, max(0, value)) }
