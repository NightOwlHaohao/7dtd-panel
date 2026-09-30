//go:build windows

package main

import (
	"os"
	"testing"
	"time"
)

// Exercises the real Windows APIs; runs on the Windows CI runner.
func TestWindowsPerformanceSourceReadsHostAndProcess(t *testing.T) {
	source, err := openPerformanceSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	time.Sleep(pdhMinInterval + 50*time.Millisecond)
	first, err := source.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMemory || first.MemoryTotal == 0 || first.MemoryAvailable > first.MemoryTotal {
		t.Fatalf("memory = %#v", first)
	}
	if !first.HasCPUTimes || first.CPUTotal == 0 {
		t.Fatalf("CPU times = %#v", first)
	}
	if !first.HasGameCPU || !first.HasGameMemory || first.GameWorkingSet == 0 {
		t.Fatalf("own process counters = %#v", first)
	}
	time.Sleep(pdhMinInterval)
	second, err := source.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUTotal <= first.CPUTotal || second.GameCPU < first.GameCPU {
		t.Fatalf("counters did not advance: %#v -> %#v", first, second)
	}
	sample := calculatePerformance(first, second, true, time.Second, 1)
	if !sample.HostCPU.Available || !sample.Memory.Available || !sample.GameMemory.Available {
		t.Fatalf("sample = %#v", sample)
	}
	// "% Processor Utility" exists on every supported Windows version; GPU
	// engine counters need a display driver, which CI runners lack.
	if !second.HasCPUUtility {
		t.Fatalf("PDH processor utility unavailable: %#v", second)
	}
	t.Logf("CPU utility %.1f%%; GPU via PDH: %v", second.CPUUtility, second.HasGPU)
}
