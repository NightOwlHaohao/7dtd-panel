package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCalculateHostCPUPrefersProcessorUtility(t *testing.T) {
	current := performanceReading{CPUUtility: 137, HasCPUUtility: true, CPUIdle: 50, CPUTotal: 100, HasCPUTimes: true}
	if got := calculateHostCPU(performanceReading{}, current, false); !got.Available || got.Value != 100 {
		t.Fatalf("utility = %#v, want clamped 100", got)
	}
}

func TestCalculateHostCPUFallsBackToSystemTimes(t *testing.T) {
	previous := performanceReading{CPUIdle: 1000, CPUTotal: 4000, HasCPUTimes: true}
	current := performanceReading{CPUIdle: 1300, CPUTotal: 5000, HasCPUTimes: true}
	if got := calculateHostCPU(previous, current, true); !got.Available || math.Abs(got.Value-70) > 1e-9 {
		t.Fatalf("system-times CPU = %#v, want 70", got)
	}
	for name, test := range map[string]struct {
		previous performanceReading
		have     bool
	}{
		"no previous":      {previous, false},
		"no progress":      {current, true},
		"idle went back":   {performanceReading{CPUIdle: 2000, CPUTotal: 4000, HasCPUTimes: true}, true},
		"previous missing": {performanceReading{}, true},
	} {
		if got := calculateHostCPU(test.previous, current, test.have); got.Available {
			t.Errorf("%s: CPU = %#v, want unavailable", name, got)
		}
	}
}

func TestCalculateGameMetrics(t *testing.T) {
	previous := performanceReading{GamePID: 42, GameCPU: 0, HasGameCPU: true}
	// 1s of CPU time (1e7 * 100ns) over 2s wall time on 4 logical CPUs = 12.5%.
	current := performanceReading{GamePID: 42, GameCPU: 10_000_000, HasGameCPU: true, GameWorkingSet: 2 << 30, HasGameMemory: true}
	if got := calculateGameCPU(previous, current, true, 2*time.Second, 4); !got.Available || math.Abs(got.Value-12.5) > 1e-9 {
		t.Fatalf("game CPU = %#v, want 12.5", got)
	}
	if got := calculateGameMemory(current); !got.Available || got.Value != 2<<30 {
		t.Fatalf("game memory = %#v", got)
	}
	restarted := current
	restarted.GamePID = 43
	if got := calculateGameCPU(previous, restarted, true, time.Second, 4); got.Available {
		t.Fatalf("CPU across a game restart must be unavailable: %#v", got)
	}
	if got := calculateGameCPU(current, previous, true, time.Second, 4); got.Available {
		t.Fatalf("CPU counter going backwards must be unavailable: %#v", got)
	}
	if got := calculateGameMemory(performanceReading{GameWorkingSet: 1, HasGameMemory: true}); got.Available {
		t.Fatalf("memory without a game PID must be unavailable: %#v", got)
	}
}

func TestCalculateMemory(t *testing.T) {
	got := calculateMemory(performanceReading{MemoryTotal: 16 << 30, MemoryAvailable: 4 << 30, HasMemory: true})
	if !got.Available || got.UsedBytes != 12<<30 || got.TotalBytes != 16<<30 || got.Percent != 75 {
		t.Fatalf("memory = %#v", got)
	}
	if got := calculateMemory(performanceReading{MemoryTotal: 1, MemoryAvailable: 2, HasMemory: true}); got.Available {
		t.Fatalf("available > total must be unavailable: %#v", got)
	}
}

func TestGPUUtilizationSumsProcessesPerEngineAndTakesBusiest(t *testing.T) {
	got, ok := gpuUtilization(map[string]float64{
		"pid_100_luid_0x0_0xC1E2_phys_0_eng_0_engtype_3D":          30,
		"pid_200_luid_0x0_0xC1E2_phys_0_eng_0_engtype_3D":          25,
		"pid_100_luid_0x0_0xC1E2_phys_0_eng_3_engtype_VideoDecode": 40,
		"_Total":  99,
		"garbage": 99,
	})
	if !ok || got != 55 {
		t.Fatalf("gpu = %v %v, want 55 (3D engine summed over processes)", got, ok)
	}
	if got, ok := gpuUtilization(map[string]float64{"pid_1_luid_a_eng_0": 80, "pid_2_luid_a_eng_0": 70}); !ok || got != 100 {
		t.Fatalf("gpu = %v %v, want clamped 100", got, ok)
	}
	if _, ok := gpuUtilization(map[string]float64{}); ok {
		t.Fatal("no engines must be unavailable")
	}
}

func TestPerformanceSampleJSONKeepsAvailableZeroValues(t *testing.T) {
	data, err := json.Marshal(PerformanceSample{
		HostCPU: MetricValue{Available: true, Value: 0},
		Memory:  MemoryValue{Available: true, UsedBytes: 0, TotalBytes: 0, Percent: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{`"value":0`, `"usedBytes":0`, `"totalBytes":0`, `"percent":0`} {
		if !strings.Contains(text, field) {
			t.Fatalf("available zero sample missing %s: %s", field, text)
		}
	}
}

type fakePerformanceSource struct {
	mu       sync.Mutex
	readings []performanceReading
	pids     []int
	err      error
	closed   int
}

func (s *fakePerformanceSource) Read(pid int) (performanceReading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pids = append(s.pids, pid)
	if s.err != nil {
		return performanceReading{}, s.err
	}
	reading := s.readings[0]
	if len(s.readings) > 1 {
		s.readings = s.readings[1:]
	}
	return reading, nil
}

func (s *fakePerformanceSource) Close() error { s.mu.Lock(); s.closed++; s.mu.Unlock(); return nil }

func testMonitor(source *fakePerformanceSource, pid int) (*PerformanceMonitor, *int) {
	opens := 0
	server := &ServerManager{state: ServerRunning, launched: &fakeLaunchedProcess{pid: pid}}
	clock := time.Unix(1000, 0)
	monitor := &PerformanceMonitor{server: server, logicalCPU: 2,
		open: func() (performanceSource, error) { opens++; return source, nil },
		now:  func() time.Time { clock = clock.Add(time.Second); return clock },
	}
	return monitor, &opens
}

func TestPerformanceMonitorOpensOnceAndDifferencesReadings(t *testing.T) {
	source := &fakePerformanceSource{readings: []performanceReading{
		{MemoryTotal: 100, MemoryAvailable: 50, HasMemory: true, GamePID: 42, GameCPU: 0, HasGameCPU: true},
		{MemoryTotal: 100, MemoryAvailable: 50, HasMemory: true, GamePID: 42, GameCPU: 10_000_000, HasGameCPU: true},
	}}
	monitor, opens := testMonitor(source, 42)
	first, err := monitor.Sample(context.Background())
	if err != nil || first.GameCPU.Available || !first.Memory.Available {
		t.Fatalf("first sample = %#v, %v", first, err)
	}
	second, err := monitor.Sample(context.Background())
	if err != nil || !second.GameCPU.Available || second.GameCPU.Value != 50 {
		t.Fatalf("second sample = %#v, %v; want game CPU 50%% (1s over 1s on 2 CPUs)", second, err)
	}
	if *opens != 1 || source.pids[0] != 42 {
		t.Fatalf("opens=%d pids=%v", *opens, source.pids)
	}
	if err := monitor.Close(); err != nil || source.closed != 1 {
		t.Fatalf("close err=%v closed=%d", err, source.closed)
	}
	if _, err := monitor.Sample(context.Background()); err == nil {
		t.Fatal("sampling after Close must fail")
	}
}

func TestPerformanceMonitorReportsUnavailableCounters(t *testing.T) {
	monitor, _ := testMonitor(&fakePerformanceSource{err: errors.New("boom")}, 42)
	if sample, err := monitor.Sample(context.Background()); err == nil || sample.Memory.Available {
		t.Fatalf("read failure = %#v, %v", sample, err)
	}
	empty, _ := testMonitor(&fakePerformanceSource{readings: []performanceReading{{}}}, 0)
	if _, err := empty.Sample(context.Background()); err == nil {
		t.Fatal("a sample with no metrics must be an error")
	}
	failing := &PerformanceMonitor{open: func() (performanceSource, error) { return nil, errors.New("no PDH") }, now: time.Now}
	if _, err := failing.Sample(context.Background()); err == nil || !strings.Contains(err.Error(), "no PDH") {
		t.Fatalf("open failure err=%v", err)
	}
}

func TestPerformanceMonitorZeroPIDLeavesOnlyGameMetricsUnavailable(t *testing.T) {
	reading := performanceReading{CPUUtility: 20, HasCPUUtility: true, MemoryTotal: 100, MemoryAvailable: 40, HasMemory: true}
	monitor, _ := testMonitor(&fakePerformanceSource{readings: []performanceReading{reading}}, 0)
	sample, err := monitor.Sample(context.Background())
	if err != nil || !sample.HostCPU.Available || !sample.Memory.Available || sample.GameCPU.Available || sample.GameMemory.Available {
		t.Fatalf("zero PID sample = %#v, %v", sample, err)
	}
}
