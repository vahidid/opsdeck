//go:build desktop && windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockData takes an exclusive lock on path, held until the process exits.
func lockData(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	var ol windows.Overlapped
	if windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol) != nil {
		f.Close()
		return false
	}
	return true // f stays open: the lock lives as long as the process
}
