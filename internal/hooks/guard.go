package hooks

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/moukrea/automodel/internal/router"
)

// Claude Code sends every request, whatever the model, to the proxy: if it
// is down, every session fails with a bare "connection refused". The
// SessionStart and UserPromptSubmit hooks check it first, restart the
// service once if needed, and otherwise say what is wrong and how to fix it.

// dial and restart are swapped in tests.
var (
	dial = func(addr string, d time.Duration) error {
		c, err := net.DialTimeout("tcp", addr, d)
		if err == nil {
			c.Close()
		}
		return err
	}
	restart = defaultRestart
)

func defaultRestart(ctx context.Context) error {
	switch runtime.GOOS {
	case "linux":
		return exec.CommandContext(ctx, "systemctl", "--user", "start", "automodel.service").Run()
	case "darwin":
		return exec.CommandContext(ctx, "launchctl", "kickstart", fmt.Sprintf("gui/%d/com.github.moukrea.automodel", os.Getuid())).Run()
	}
	return fmt.Errorf("no service manager")
}

// proxyProblem returns "" when Claude Code's API endpoint is fine, else a
// message for the user. Sessions pointed somewhere else are not our concern.
func proxyProblem(ctx context.Context, env *router.Env) string {
	addr := env.Cfg.Listen
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Host != addr {
			return ""
		}
	}
	if dial(addr, 300*time.Millisecond) == nil {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if restart(rctx) == nil {
		for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if dial(addr, 300*time.Millisecond) == nil {
				return ""
			}
		}
	}
	fix := "systemctl --user restart automodel (logs: journalctl --user -u automodel)"
	if runtime.GOOS == "darwin" {
		fix = "launchctl kickstart -k gui/$(id -u)/com.github.moukrea.automodel"
	}
	return fmt.Sprintf("automodel's local proxy (%s) is not answering, so Claude Code can't reach the API. "+
		"Start it with: %s. Or run `automodel doctor`, or remove automodel with `automodel uninstall`.", addr, fix)
}
