//go:build desktop && !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockData takes an exclusive lock on path, held until the process exits.
func lockData(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return false
	}
	return true // f stays open: the lock lives as long as the process
}
