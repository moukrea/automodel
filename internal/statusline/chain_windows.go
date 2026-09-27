package statusline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// startChain runs the user's statusline command the way Claude Code runs
// it on Windows: with Git Bash, or PowerShell when Git Bash isn't there.
// Stdin comes from in; the output goes to tmp, then moves to out, and the
// running marker is removed. It does not wait.
func startChain(command, in, tmp, out, running string) {
	var cmd *exec.Cmd
	if bash := gitBash(); bash != "" && !noGitBash {
		slash := filepath.ToSlash
		script := fmt.Sprintf("(%s) < %q > %q 2>/dev/null; mv -f %q %q; rm -f %q",
			command, slash(in), slash(tmp), slash(tmp), slash(out), slash(running))
		cmd = exec.Command(bash, "-c", script)
	} else {
		f, err := os.Open(in)
		if err != nil {
			return
		}
		defer f.Close()
		script := fmt.Sprintf("$ErrorActionPreference = 'SilentlyContinue'; "+
			"$o = (& { %s } 2>$null | Out-String); "+
			"[IO.File]::WriteAllText(%s, $o); "+
			"Move-Item -Force -LiteralPath %s -Destination %s; Remove-Item -Force -LiteralPath %s",
			command, psQuote(tmp), psQuote(tmp), psQuote(out), psQuote(running))
		cmd = exec.Command(powershell(), "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Stdin = f
	}
	// No console window, and out of the caller's job when it allows it:
	// Claude Code may kill the statusline's job once it has printed.
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	cmd.Env = chainedEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	if cmd.Start() != nil {
		retry := exec.Command(cmd.Path, cmd.Args[1:]...)
		retry.Stdin = cmd.Stdin
		retry.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if retry.Start() != nil {
			return
		}
		cmd = retry
	}
	cmd.Process.Release()
}

// noGitBash makes tests take the PowerShell path.
var noGitBash bool

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func powershell() string {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	return "powershell"
}

// gitBash finds Git for Windows' bash as Claude Code does:
// $CLAUDE_CODE_GIT_BASH_PATH, next to git on the PATH, or the usual install
// folders. bash.exe in System32 is WSL's, not Git Bash.
func gitBash() string {
	exists := func(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" && exists(p) {
		return p
	}
	var candidates []string
	if git, err := exec.LookPath("git"); err == nil {
		// <Git>\cmd\git.exe, <Git>\bin\git.exe or <Git>\mingw64\bin\git.exe
		d := filepath.Dir(filepath.Dir(git))
		candidates = append(candidates, filepath.Join(d, "bin", "bash.exe"), filepath.Join(filepath.Dir(d), "bin", "bash.exe"))
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if v := os.Getenv(env); v != "" {
			candidates = append(candidates, filepath.Join(v, "Git", "bin", "bash.exe"))
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		candidates = append(candidates, filepath.Join(v, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, c := range candidates {
		if exists(c) {
			return c
		}
	}
	return ""
}
