//go:build !windows

package main

import "errors"

func openPerformanceSource() (performanceSource, error) {
	return nil, errors.New("performance monitoring is available on Windows only")
}
