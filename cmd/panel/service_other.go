//go:build !windows

package main

import "context"

func runAsService(func(context.Context) error) (bool, error) { return false, nil }
