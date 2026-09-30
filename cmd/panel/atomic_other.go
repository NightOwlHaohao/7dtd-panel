//go:build !windows

package main

import "os"

// replaceFile atomically replaces targetPath; rename(2) already has these semantics.
func replaceFile(tempPath, targetPath string) error { return os.Rename(tempPath, targetPath) }
