//go:build windows

package main

import "syscall"

func modPathIsReparse(path string) (bool, error) {
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := syscall.GetFileAttributes(path16)
	if err != nil {
		return false, err
	}
	return attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
