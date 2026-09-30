//go:build !windows

package main

func availableImportSpace(string) (uint64, error) { return ^uint64(0), nil }
