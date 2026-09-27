//go:build !windows

package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// isProxy reports whether pid runs `automodel serve` (nothing to check
// without /proc or ps: trust the pidfile).
func isProxy(pid int) bool {
	c := cmdline(pid)
	return c == "" || (strings.Contains(c, "automodel") && strings.Contains(c, "serve"))
}

func cmdline(pid int) string {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		return strings.ReplaceAll(string(b), "\x00", " ")
	}
	out, _ := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// stopProcess asks pid to exit, then kills it after 5 seconds.
func stopProcess(pid int) {
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50 && processAlive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
}

// startDetachedProcess starts cmd in its own session, so it outlives the
// hook or the terminal that started it.
func startDetachedProcess(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}

// The logon entry exists on Windows only.
const logonEntry = "logon entry"

func logonInstalled() bool         { return false }
func logonCommand() (string, bool) { return "", false }
func (o Options) logonCmd() string { return "" }
func removeLogon() bool            { return false }
func registerLogon(o Options) error {
	return errors.New("starting at logon is only supported on Windows")
}

// cmdArg quotes a path for the POSIX shell that runs hook and statusline
// commands; plain paths are left as they are.
func cmdArg(p string) string {
	if p != "" && strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+,:@%=") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
