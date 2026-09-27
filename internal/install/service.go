package install

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moukrea/automodel/internal/config"
)

// How the proxy runs. The proxy reads it from $AUTOMODEL_SERVICE to know
// whether a service manager restarts it after an exit.
const (
	Systemd  = "systemd"
	Launchd  = "launchd"
	Detached = "detached" // background process started by install, no supervisor
)

// ServiceEnv tells the proxy which of the above runs it.
const ServiceEnv = "AUTOMODEL_SERVICE"

// Runner runs a command and returns its combined output; tests replace it.
type Runner func(name string, args ...string) ([]byte, error)

func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// ServiceMode picks how the proxy runs on goos: launchd on macOS, a systemd
// user unit when a user manager answers, else a detached process (containers,
// WSL1, minimal distros).
func ServiceMode(goos string, run Runner) string {
	switch goos {
	case "darwin":
		return Launchd
	case "linux":
		// Fails when systemctl is missing or there is no user bus ("Failed to
		// connect to bus"). is-system-running would also fail on a merely
		// degraded manager, which still runs units.
		if _, err := run("systemctl", "--user", "show-environment"); err == nil {
			return Systemd
		}
	}
	return Detached
}

func (o Options) mode() string { return ServiceMode(o.goos(), o.runner()) }

// InstalledMode is how the proxy was installed, from what install left:
// the systemd unit, the launchd agent, or the detached proxy's pidfile. The
// environment can differ later (an SSH login without a user bus, a desktop
// session with one), so restarts and checks follow the install, and fall
// back to what the environment offers when nothing is found.
func InstalledMode(goos, unitPath string, cfg *config.Config, run Runner) string {
	exists := func(p string) bool { _, err := os.Stat(p); return p != "" && err == nil }
	switch {
	case goos == "darwin" && exists(LaunchdPlist()):
		return Launchd
	case goos == "linux" && exists(unitPath):
		return Systemd
	case exists(PidFile(cfg)):
		return Detached
	}
	return ServiceMode(goos, run)
}

// DefaultUnitPath is where install writes the systemd user unit.
func DefaultUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", unitName)
}

// PidFile records the pid of the detached proxy.
func PidFile(cfg *config.Config) string { return filepath.Join(cfg.StateDir, "proxy.pid") }

// LogFile is where the detached proxy and the launchd agent log.
func LogFile(cfg *config.Config) string { return filepath.Join(cfg.StateDir, "proxy.log") }

// DetachedPid returns the pid of the running detached proxy, or 0.
func DetachedPid(cfg *config.Config) int {
	b, err := os.ReadFile(PidFile(cfg))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	// A reused pid must not be taken for ours (nothing to check without
	// /proc or ps: trust the pidfile).
	if c := cmdline(pid); c != "" && !(strings.Contains(c, "automodel") && strings.Contains(c, "serve")) {
		return 0
	}
	return pid
}

func cmdline(pid int) string {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		return strings.ReplaceAll(string(b), "\x00", " ")
	}
	out, _ := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// StopDetached stops the detached proxy, if one runs, and reports whether it
// did.
func StopDetached(cfg *config.Config) bool {
	pid := DetachedPid(cfg)
	os.Remove(PidFile(cfg))
	if pid == 0 {
		return false
	}
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	return true
}

// startDetached runs `automodel serve` in its own session, logging to the
// state dir, and records its pid. Nothing restarts it after a reboot.
func startDetached(o Options, cfg *config.Config) error {
	StopDetached(cfg)
	if listening(cfg.Listen) {
		return fmt.Errorf("%s is already in use (another `automodel serve`?); settings left untouched", cfg.Listen)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(LogFile(cfg), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(o.Exe, "--config", o.ConfigPath, "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), ServiceEnv+"="+Detached)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reaps it if it exits while we still run
	if err := os.WriteFile(PidFile(cfg), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}
	if err := waitListening(o, cfg, fmt.Sprintf("background proxy (pid %d)", pid)); err != nil {
		return fmt.Errorf("%w\n%s", err, tail(LogFile(cfg), 20))
	}
	start := o.StartCmd()
	o.Log("No systemd user session here: the proxy runs as a background process (log %s)", LogFile(cfg))
	o.Log("and will NOT be restarted after a reboot. Start it at each login with:")
	o.Log("  %s", start)
	o.Log("for instance with this line in ~/.profile:")
	o.Log("  %s >/dev/null 2>&1", start)
	return nil
}

// Relaunch restarts the detached proxy of an install without a service
// manager (the hooks' guard, after it died). Without a pidfile the proxy
// wasn't installed that way: an error.
func Relaunch(cfg *config.Config) error {
	if _, err := os.Stat(PidFile(cfg)); err != nil {
		return errors.New("no service manager and no background proxy installed")
	}
	o := Options{Exe: Self(), ConfigPath: cfg.Path(), Log: func(string, ...any) {}}
	return startDetached(o, cfg)
}

// Self is the path of this binary, with a Homebrew keg path (which changes
// on every upgrade) mapped to its link in the prefix, so hooks and services
// survive `brew upgrade`.
func Self() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if i := strings.Index(exe, "/Cellar/"); i >= 0 {
		link := filepath.Join(exe[:i], "bin", filepath.Base(exe))
		if _, err := os.Stat(link); err == nil {
			return link
		}
	}
	return exe
}

// StartCmd is the command that starts the proxy if it isn't running.
func (o Options) StartCmd() string {
	return fmt.Sprintf("%s --config %s start", o.Exe, o.ConfigPath)
}

// Start makes sure the proxy runs: it is what to run at login when no
// service manager keeps it up. A running proxy is left alone.
func Start(o Options, cfg *config.Config) error {
	if listening(cfg.Listen) {
		o.Log("proxy already running on %s", cfg.Listen)
		return nil
	}
	return startService(o, cfg)
}

// ServiceStatus reports how the proxy runs and whether that is healthy.
func ServiceStatus(o Options, cfg *config.Config) (mode, detail string, ok bool) {
	run := o.runner()
	switch mode = InstalledMode(o.goos(), o.UnitPath, cfg, run); mode {
	case Systemd:
		if _, err := os.Stat(o.UnitPath); err != nil {
			return mode, "unit " + o.UnitPath + " not installed", false
		}
		out, err := run("systemctl", "--user", "is-active", unitName)
		return mode, unitName + " " + strings.TrimSpace(string(out)), err == nil
	case Launchd:
		if _, err := os.Stat(LaunchdPlist()); err != nil {
			return mode, "agent " + LaunchdPlist() + " not installed", false
		}
		if _, err := run("launchctl", "list", launchdLabel); err != nil {
			return mode, "agent " + launchdLabel + " not loaded", false
		}
		return mode, "agent " + launchdLabel + " loaded", true
	}
	if pid := DetachedPid(cfg); pid > 0 {
		return mode, fmt.Sprintf("background process, pid %d (not restarted after a reboot)", pid), true
	}
	return mode, "no background process (" + PidFile(cfg) + ")", false
}

func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		c.Close()
	}
	return err == nil
}

func tail(path string, n int) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
