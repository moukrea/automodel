package install

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/config"
)

// With $AUTOMODEL_TEST_SERVE set, the test binary stands in for
// `automodel serve`: it listens there until killed.
func TestMain(m *testing.M) {
	if addr := os.Getenv("AUTOMODEL_TEST_SERVE"); addr != "" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			os.Exit(3)
		}
		http.Serve(l, http.NotFoundHandler())
		return
	}
	os.Exit(m.Run())
}

// fakeRun answers systemctl as a host with (ok) or without a user manager.
func fakeRun(ok bool, calls *[]string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if name == "systemctl" && !ok {
			return []byte("Failed to connect to bus: No medium found"), errors.New("exit status 1")
		}
		return nil, nil
	}
}

func TestServiceMode(t *testing.T) {
	missing := func(string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	var calls []string
	for _, c := range []struct {
		goos string
		run  Runner
		want string
	}{
		{"darwin", missing, Launchd},
		{"linux", fakeRun(true, &calls), Systemd},
		{"linux", fakeRun(false, &calls), Detached},
		{"linux", missing, Detached},
		{"freebsd", fakeRun(true, &calls), Detached},
	} {
		if got := ServiceMode(c.goos, c.run); got != c.want {
			t.Errorf("%s: %s, want %s", c.goos, got, c.want)
		}
	}
	if calls[0] != "systemctl --user show-environment" {
		t.Errorf("probe: %q", calls[0])
	}
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Without a user manager, install runs the proxy detached, records its pid,
// leaves a running one alone on start, and stops it by the pidfile.
func TestDetachedLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StateDir = dir
	cfg.Listen = freeAddr(t)
	t.Setenv("AUTOMODEL_TEST_SERVE", cfg.Listen)
	var calls []string
	o := Options{Exe: os.Args[0], ConfigPath: filepath.Join(dir, "automodel", "config.toml"),
		UnitPath: filepath.Join(dir, "automodel.service"), GOOS: "linux", Run: fakeRun(false, &calls), Log: t.Logf}
	if err := startService(o, cfg); err != nil {
		t.Fatal(err)
	}
	pid := DetachedPid(cfg)
	if pid == 0 || !listening(cfg.Listen) {
		t.Fatalf("pid %d, listening %v", pid, listening(cfg.Listen))
	}
	if mode, detail, ok := ServiceStatus(o, cfg); mode != Detached || !ok {
		t.Errorf("status: %s %s %v", mode, detail, ok)
	}
	if err := Start(o, cfg); err != nil || DetachedPid(cfg) != pid {
		t.Errorf("start restarted a running proxy: %v, pid %d → %d", err, pid, DetachedPid(cfg))
	}
	if !StopDetached(cfg) || listening(cfg.Listen) {
		t.Error("not stopped")
	}
	if _, err := os.Stat(PidFile(cfg)); !os.IsNotExist(err) {
		t.Errorf("pidfile left: %v", err)
	}
	if _, _, ok := ServiceStatus(o, cfg); ok {
		t.Error("status ok after stop")
	}
}

// The hooks' guard relaunches a detached proxy that died, and only when the
// install is a detached one.
func TestRelaunch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "automodel", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, nil, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = dir
	cfg.Listen = freeAddr(t)
	t.Setenv("AUTOMODEL_TEST_SERVE", cfg.Listen)
	if Relaunch(cfg) == nil {
		t.Fatal("relaunched without a detached install")
	}
	o := Options{Exe: os.Args[0], ConfigPath: path, Log: t.Logf}
	if err := startDetached(o, cfg); err != nil {
		t.Fatal(err)
	}
	pid := DetachedPid(cfg)
	syscall.Kill(pid, syscall.SIGKILL)
	for i := 0; i < 50 && listening(cfg.Listen); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err := Relaunch(cfg); err != nil || !listening(cfg.Listen) || DetachedPid(cfg) == pid {
		t.Fatalf("relaunch: %v, listening %v", err, listening(cfg.Listen))
	}
	StopDetached(cfg)
}

// A pidfile naming someone else's process (pid reuse) is ignored.
func TestDetachedPidIgnoresOtherProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	// Right after fork the child may not have exec'd sleep yet (empty or
	// parent command line): wait until it has.
	for deadline := time.Now().Add(3 * time.Second); !strings.Contains(cmdline(cmd.Process.Pid), "sleep") && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	os.WriteFile(PidFile(cfg), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	if pid := DetachedPid(cfg); pid != 0 {
		t.Errorf("took sleep (%d) for the proxy", pid)
	}
}
