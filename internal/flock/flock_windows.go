// Package flock takes an exclusive advisory lock on an open file, across
// processes: flock(2) on Unix, LockFileEx on Windows.
package flock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows byte-range locks are mandatory: the lock covers one byte far past
// any end of file, so it never blocks reads or appends through other
// handles, only other Lock calls.
const lockOffsetHigh = 0x7fffffff

// Lock blocks until f is locked exclusively and returns the unlock function.
// The lock is taken on a handle of its own: f may have been opened for
// appending only, which LockFileEx does not accept.
func Lock(f *os.File) (unlock func(), err error) {
	h, err := os.OpenFile(f.Name(), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	if err := windows.LockFileEx(windows.Handle(h.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		h.Close()
		return nil, err
	}
	return func() {
		windows.UnlockFileEx(windows.Handle(h.Fd()), 0, 1, 0, &windows.Overlapped{OffsetHigh: lockOffsetHigh})
		h.Close()
	}, nil
}
