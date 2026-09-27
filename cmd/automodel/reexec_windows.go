package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// reexec starts exe with the same arguments as a new background process
// (Windows has no exec) and hands it the pidfile; the caller then exits,
// and the new proxy binds the port once it is free.
func reexec(exe string) error {
	newCmd := func(flags uint32) *exec.Cmd {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), restartedEnv+"=1")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr // the proxy log
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return cmd
	}
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	cmd := newCmd(flags | windows.CREATE_BREAKAWAY_FROM_JOB)
	if err := cmd.Start(); err != nil {
		cmd = newCmd(flags)
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	if b, err := os.ReadFile(servingPidFile); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
		os.WriteFile(servingPidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
	}
	cmd.Process.Release()
	return nil
}
