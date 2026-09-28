package hooks

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Out of Claude Code's job and console, so it outlives the hook.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_BREAKAWAY_FROM_JOB}
}
