package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const stillActive = 259 // STILL_ACTIVE, the exit code of a running process

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

// cmdline is the executable of pid (Windows has no cheap way to read
// another process's arguments).
func cmdline(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// isProxy reports whether pid runs an automodel binary (or this one: the
// tests run themselves as the proxy).
func isProxy(pid int) bool {
	c := cmdline(pid)
	if c == "" {
		return true
	}
	name := strings.ToLower(filepath.Base(c))
	self, _ := os.Executable()
	return strings.Contains(name, "automodel") || name == strings.ToLower(filepath.Base(self))
}

// stopProcess terminates pid (Windows has no SIGTERM for a process without
// a console) and waits up to 5 seconds for it to exit.
func stopProcess(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	windows.TerminateProcess(h, 1)
	windows.WaitForSingleObject(h, 5000)
	for i := 0; i < 20 && processAlive(pid); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

// startDetachedProcess starts cmd without a console window, in a process
// group of its own, and out of the job of the process that started it when
// that job allows it: Claude Code's hooks may run in a job that is killed
// with them, and the relaunched proxy must outlive the hook.
func startDetachedProcess(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err == nil {
		return cmd, nil
	}
	// The job forbids breaking away: stay in it.
	cmd = newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	return cmd, cmd.Start()
}

// The proxy starts at each logon through the per-user Run key: no admin
// rights, no scheduled task.
const runValue = "automodel"

var (
	runKey     = `Software\Microsoft\Windows\CurrentVersion\Run` // replaced in tests
	logonEntry = `HKCU\` + runKey + `\` + runValue
)

func (o Options) logonCmd() string {
	return syscall.EscapeArg(o.Exe) + " --config " + syscall.EscapeArg(o.ConfigPath) + " start"
}

func logonCommand() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return v, err == nil
}

func logonInstalled() bool { _, ok := logonCommand(); return ok }

func registerLogon(o Options) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runValue, o.logonCmd())
}

func removeLogon() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	return k.DeleteValue(runValue) == nil
}

// cmdArg writes a path for the shell Claude Code runs hook and statusline
// commands with on Windows: Git Bash, or PowerShell without it. Forward
// slashes, since Git Bash eats unquoted backslashes. A path with spaces or
// shell characters gets the short (8.3) name of its directory, which both
// shells take unquoted (the file name is kept: settings entries are found
// by it); if there is none, double quotes, which only Git Bash takes.
func cmdArg(p string) string {
	if p == "" {
		return `""`
	}
	if plain(p) {
		return filepath.ToSlash(p)
	}
	dir, file := filepath.Split(p)
	if short, err := shortPath(dir); err == nil && plain(short+file) {
		return filepath.ToSlash(filepath.Join(short, file))
	}
	return `"` + filepath.ToSlash(p) + `"`
}

func plain(p string) bool {
	return strings.Trim(p, `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/\._-+:~=`) == ""
}

func shortPath(p string) (string, error) {
	from, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(from, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}
