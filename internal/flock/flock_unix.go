//go:build !windows

// Package flock takes an exclusive advisory lock on an open file, across
// processes: flock(2) on Unix, LockFileEx on Windows.
package flock

import (
	"os"
	"syscall"
)

// Lock blocks until f is locked exclusively and returns the unlock function.
func Lock(f *os.File) (unlock func(), err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
