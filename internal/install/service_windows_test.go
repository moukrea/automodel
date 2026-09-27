package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/moukrea/automodel/internal/config"
)

// The tests never touch the real Run key.
const testRunKey = `Software\automodel-test\Run`

func init() { runKey, logonEntry = testRunKey, `HKCU\`+testRunKey+`\`+runValue }

// On Windows install runs the proxy in the background and registers it to
// start at each logon; uninstall removes both.
func TestLogonLifecycle(t *testing.T) {
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, testRunKey)
		registry.DeleteKey(registry.CURRENT_USER, `Software\automodel-test`)
	})
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StateDir = dir
	cfg.Listen = freeAddr(t)
	t.Setenv("AUTOMODEL_TEST_SERVE", cfg.Listen)
	o := Options{Exe: os.Args[0], ConfigPath: filepath.Join(dir, "automodel", "config.toml"), Log: t.Logf}
	if m := ServiceMode("windows", nil); m != Logon {
		t.Fatalf("mode %s", m)
	}
	if err := startService(o, cfg); err != nil {
		t.Fatal(err)
	}
	if got, ok := logonCommand(); !ok || got != o.logonCmd() || !strings.HasSuffix(got, " start") {
		t.Errorf("logon entry %q %v", got, ok)
	}
	if m := InstalledMode("windows", "", cfg, nil); m != Logon {
		t.Errorf("installed mode %s", m)
	}
	if mode, detail, ok := ServiceStatus(o, cfg); mode != Logon || !ok {
		t.Errorf("status: %s %s %v", mode, detail, ok)
	}
	pid := DetachedPid(cfg)
	if pid == 0 || !listening(cfg.Listen) {
		t.Fatalf("pid %d, listening %v", pid, listening(cfg.Listen))
	}
	// The hooks' guard relaunches it after it died.
	stopProcess(pid)
	for i := 0; i < 50 && listening(cfg.Listen); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err := Relaunch(cfg); err != nil || !listening(cfg.Listen) || DetachedPid(cfg) == pid {
		t.Fatalf("relaunch: %v, listening %v", err, listening(cfg.Listen))
	}
	if !removeLogon() || logonInstalled() {
		t.Error("logon entry not removed")
	}
	if !StopDetached(cfg) || listening(cfg.Listen) {
		t.Error("not stopped")
	}
	if _, _, ok := ServiceStatus(o, cfg); ok {
		t.Error("status ok after uninstall")
	}
}

// Hook commands name paths with spaces so that both Git Bash and PowerShell
// run them: forward slashes, and a short directory name or quotes.
func TestCmdArgSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "John Doe", "bin")
	os.MkdirAll(dir, 0o700)
	exe := filepath.Join(dir, "automodel.exe")
	os.WriteFile(exe, nil, 0o700)
	got := cmdArg(exe)
	if strings.Contains(got, `\`) || !strings.HasSuffix(strings.Trim(got, `"`), "/automodel.exe") {
		t.Fatalf("cmdArg(%q) = %q", exe, got)
	}
	if strings.HasPrefix(got, `"`) {
		t.Logf("no short names on this volume: %s", got)
	} else if strings.Contains(got, " ") {
		t.Fatalf("unquoted space: %q", got)
	} else if _, err := os.Stat(got); err != nil {
		t.Fatalf("short path %q: %v", got, err)
	}
	if got := cmdArg(`C:\Users\jd\AppData\Local\automodel\bin\automodel.exe`); got != "C:/Users/jd/AppData/Local/automodel/bin/automodel.exe" {
		t.Errorf("plain path: %q", got)
	}
	// Git Bash, when there, runs it.
	if bash, err := exec.LookPath("bash"); err == nil && !strings.Contains(strings.ToLower(bash), `\system32\`) {
		if out, err := exec.Command(bash, "-c", "test -f "+got+" && echo ok").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "ok" {
			t.Errorf("bash: %s %v", out, err)
		}
	}
}
