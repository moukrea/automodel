//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process with exe, same arguments and environment:
// the pid stays the same, so the pidfile stays right. It only returns on
// failure.
func reexec(exe string) error {
	return syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}
