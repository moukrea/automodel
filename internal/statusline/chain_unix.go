//go:build !windows

package statusline

import (
	"fmt"
	"os/exec"
	"syscall"
)

// startChain runs the user's statusline command in its own session with
// stdin from in, then moves its output from tmp to out and removes the
// running marker. It does not wait.
func startChain(command, in, tmp, out, running string) {
	script := fmt.Sprintf("(%s) < %q > %q 2>/dev/null; mv -f %q %q; rm -f %q", command, in, tmp, tmp, out, running)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = chainedEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}
