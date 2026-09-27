// Package install wires automodel into Claude Code (settings.json), writes
// its config and runs the proxy as a systemd user service — and undoes it.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/config"
)

type Options struct {
	Exe          string // absolute path of the automodel binary
	ConfigPath   string
	CatalogPath  string // used when creating the config
	SettingsPath string // ~/.claude/settings.json
	UnitPath     string // ~/.config/systemd/user/automodel.service
	Log          func(format string, a ...any)
}

const unitName = "automodel.service"

// Marker identifies settings entries we own.
func (o Options) hookCmd(name string) string {
	return fmt.Sprintf("%s --config %s hook %s", o.Exe, o.ConfigPath, name)
}

func (o Options) statuslineCmd() string {
	return fmt.Sprintf("%s --config %s statusline", o.Exe, o.ConfigPath)
}

func owned(cmd string) bool {
	return strings.Contains(cmd, "automodel") && (strings.Contains(cmd, " hook ") || strings.HasSuffix(cmd, " statusline"))
}

var hookEvents = []struct{ event, matcher, name string }{
	{"UserPromptSubmit", "", "decide"},
	{"PreToolUse", "Agent|Task", "agent"},
	{"PreToolUse", "Workflow", "workflow"},
	{"PreCompact", "", "precompact"},
	{"SessionStart", "", "session-start"},
	{"PostModelSwitch", "", "model-switch"},
}

// firstPartyEnv tells Claude Code the base URL is Anthropic's API. Any other
// ANTHROPIC_BASE_URL makes it assume a third-party gateway in every session,
// routed or not: Opus loses its native 1M window (auto-compact at 200k), and
// tool search and other first-party features turn off. Only true when the
// proxy forwards to api.anthropic.com unchanged.
const firstPartyEnv = "_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL"

func firstPartyUpstream(cfg *config.Config) bool {
	u, err := url.Parse(cfg.Upstream)
	return err == nil && u.Scheme == "https" && u.Host == "api.anthropic.com"
}

func envVars(cfg *config.Config) [][2]string {
	vars := [][2]string{
		{"ANTHROPIC_BASE_URL", "http://" + cfg.Listen},
		{"ANTHROPIC_CUSTOM_MODEL_OPTION", cfg.CustomModelID},
		{"ANTHROPIC_CUSTOM_MODEL_OPTION_NAME", "Jev (auto)"},
		{"ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION", "Model and effort chosen automatically"},
		{"ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES", "effort,xhigh_effort,max_effort,thinking,adaptive_thinking,interleaved_thinking"},
		{"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "1000000"},
	}
	if firstPartyUpstream(cfg) {
		vars = append(vars, [2]string{firstPartyEnv, "1"})
	}
	return vars
}

// Apply installs everything. The proxy is started and checked before
// settings.json points Claude Code at it.
func Apply(o Options) error {
	settings, raw, err := readSettings(o.SettingsPath)
	if err != nil {
		return err
	}
	prevStatusline := ""
	if sl, ok := settings.Get("statusLine"); ok {
		if slo, ok := sl.(*Object); ok {
			if c, _ := slo.Get("command"); c != nil && !owned(fmt.Sprint(c)) {
				prevStatusline = fmt.Sprint(c)
			}
		}
	}
	if err := writeConfig(o, prevStatusline); err != nil {
		return err
	}
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	if err := startService(o, cfg); err != nil {
		return err
	}
	if raw != nil {
		backup := fmt.Sprintf("%s.automodel-backup-%s", o.SettingsPath, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return err
		}
		o.Log("settings backup: %s", backup)
	}
	merge(settings, o, cfg)
	if err := writeSettings(o.SettingsPath, settings); err != nil {
		return err
	}
	o.Log("settings updated: %s", o.SettingsPath)
	return nil
}

// Preview returns the settings.json entries Apply merges, as JSON.
func Preview(o Options, cfg *config.Config) ([]byte, error) {
	s := NewObject()
	merge(s, o, cfg)
	return json.MarshalIndent(s, "", "  ")
}

func merge(s *Object, o Options, cfg *config.Config) {
	env := s.Obj("env")
	env.Delete(firstPartyEnv) // re-added below only for a first-party upstream
	for _, kv := range envVars(cfg) {
		env.Set(kv[0], kv[1])
	}
	perms := s.Obj("permissions")
	allow, _ := perms.Get("allow")
	list, _ := allow.([]any)
	if !containsAny(list, "Workflow") {
		perms.Set("allow", append(list, "Workflow"))
	}
	sl := s.Obj("statusLine")
	sl.Set("type", "command")
	sl.Set("command", o.statuslineCmd())
	hooks := s.Obj("hooks")
	removeOwnedHooks(hooks)
	for _, h := range hookEvents {
		groups, _ := hooks.Get(h.event)
		list, _ := groups.([]any)
		g := NewObject()
		if h.matcher != "" {
			g.Set("matcher", h.matcher)
		}
		cmd := NewObject()
		cmd.Set("type", "command")
		cmd.Set("command", o.hookCmd(h.name))
		cmd.Set("timeout", 15)
		g.Set("hooks", []any{cmd})
		hooks.Set(h.event, append(list, g))
	}
}

// Remove undoes Apply: settings entries, statusline (restored), service.
func Remove(o Options) error {
	settings, raw, err := readSettings(o.SettingsPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		cfg = config.Default()
	}
	if raw != nil {
		env := settings.Obj("env")
		for _, kv := range envVars(cfg) {
			env.Delete(kv[0])
		}
		env.Delete(firstPartyEnv)
		if env.Len() == 0 {
			settings.Delete("env")
		}
		hooks := settings.Obj("hooks")
		removeOwnedHooks(hooks)
		if hooks.Len() == 0 {
			settings.Delete("hooks")
		}
		if sl, ok := settings.Get("statusLine"); ok {
			if slo, ok := sl.(*Object); ok {
				if c, _ := slo.Get("command"); owned(fmt.Sprint(c)) {
					if cfg.StatuslineCommand != "" {
						slo.Set("command", cfg.StatuslineCommand)
					} else {
						settings.Delete("statusLine")
					}
				}
			}
		}
		// "Workflow" in permissions.allow is left: it may predate automodel.
		backup := fmt.Sprintf("%s.automodel-backup-%s", o.SettingsPath, time.Now().Format("20060102-150405"))
		os.WriteFile(backup, raw, 0o600)
		if err := writeSettings(o.SettingsPath, settings); err != nil {
			return err
		}
		o.Log("settings restored (backup %s)", backup)
	}
	if runtime.GOOS == "darwin" {
		exec.Command("launchctl", "unload", "-w", LaunchdPlist()).Run()
		os.Remove(LaunchdPlist())
		o.Log("launchd agent removed")
	}
	if runtime.GOOS == "linux" {
		exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
		os.Remove(o.UnitPath)
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		o.Log("service stopped and removed")
	}
	o.Log("config and state kept: %s, %s", o.ConfigPath, cfg.StateDir)
	return nil
}

func removeOwnedHooks(hooks *Object) {
	for _, ev := range append([]string{}, hooks.keys...) {
		groups, _ := hooks.Get(ev)
		list, ok := groups.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range list {
			gobj, ok := g.(*Object)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, _ := gobj.Get("hooks")
			hl, _ := hs.([]any)
			var keepH []any
			for _, h := range hl {
				if ho, ok := h.(*Object); ok {
					if c, _ := ho.Get("command"); owned(fmt.Sprint(c)) {
						continue
					}
				}
				keepH = append(keepH, h)
			}
			if len(keepH) == 0 && len(hl) > 0 {
				continue
			}
			if len(keepH) != len(hl) {
				gobj.Set("hooks", keepH)
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			hooks.Delete(ev)
		} else {
			hooks.Set(ev, kept)
		}
	}
}

func writeConfig(o Options, prevStatusline string) error {
	if _, err := os.Stat(o.ConfigPath); err == nil {
		o.Log("config kept: %s", o.ConfigPath)
		return os.Chmod(o.ConfigPath, 0o600)
	}
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# automodel runtime config (models live in the catalog). See config.example.toml.\n")
	fmt.Fprintf(&b, "catalog = %q\n", o.CatalogPath)
	if prevStatusline != "" {
		fmt.Fprintf(&b, "# your previous statusline, rendered before the automodel segment\n")
		fmt.Fprintf(&b, "statusline_command = %q\n", prevStatusline)
	}
	fmt.Fprintf(&b, "\n# OpenRouter key for Jev decisions ($OPENROUTER_API_KEY wins if set). Keep this file 0600.\n")
	fmt.Fprintf(&b, "openrouter_api_key = \"\"\n")
	if err := os.WriteFile(o.ConfigPath, []byte(b.String()), 0o600); err != nil {
		return err
	}
	o.Log("config written: %s", o.ConfigPath)
	return nil
}

func startService(o Options, cfg *config.Config) error {
	if runtime.GOOS == "darwin" {
		return startLaunchd(o, cfg)
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("automatic service setup supports Linux (systemd) and macOS (launchd); run `%s --config %s serve` yourself", o.Exe, o.ConfigPath)
	}
	unit := fmt.Sprintf(`[Unit]
Description=automodel proxy for Claude Code (jev model routing)
After=network-online.target

[Service]
ExecStart=%s --config %s serve
Restart=always
RestartSec=1

[Install]
WantedBy=default.target
`, o.Exe, o.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(o.UnitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(o.UnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if err := waitListening(o, cfg, "service "+unitName); err != nil {
		out, _ := exec.Command("journalctl", "--user", "-u", unitName, "-n", "20", "--no-pager").CombinedOutput()
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func readSettings(path string) (*Object, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewObject(), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	o, err := ParseObject(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, raw, nil
}

func writeSettings(path string, s *Object) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".automodel-tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

const launchdLabel = "com.github.moukrea.automodel"

// LaunchdPlist is where the macOS agent is installed.
func LaunchdPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func startLaunchd(o Options, cfg *config.Config) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>--config</string><string>%s</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchdLabel, o.Exe, o.ConfigPath, filepath.Join(cfg.StateDir, "proxy.log"))
	path := LaunchdPlist()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	exec.Command("launchctl", "unload", path).Run()
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %v: %s", err, out)
	}
	return waitListening(o, cfg, "launchd agent "+launchdLabel)
}

func waitListening(o Options, cfg *config.Config, what string) error {
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", cfg.Listen, 200*time.Millisecond); err == nil {
			c.Close()
			o.Log("%s running on %s", what, cfg.Listen)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("proxy not listening on %s; settings left untouched", cfg.Listen)
}

// SeedCatalog writes the catalog shipped in the binary to path when it is
// missing, and replaces it with a newer shipped one while the user hasn't
// edited it (it still hashes to the last shipped copy). It reports whether
// the file changed.
func SeedCatalog(path string, shipped []byte, stateDir string) (bool, error) {
	mark := filepath.Join(stateDir, "catalog.shipped.sha256")
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	cur, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return false, err
	default:
		last, _ := os.ReadFile(mark)
		if sum(cur) == sum(shipped) || sum(cur) != strings.TrimSpace(string(last)) {
			return false, nil // up to date, or edited by the user: kept
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, shipped, 0o600); err != nil {
		return false, err
	}
	os.MkdirAll(stateDir, 0o700)
	return true, os.WriteFile(mark, []byte(sum(shipped)+"\n"), 0o600)
}

// Refresh re-applies the settings entries after a self-update (a new
// release may add hooks or environment variables). Settings are only
// rewritten when they change; the service is left alone.
// ownedBy reports whether settings already run this install: its statusline
// or one of its hooks calls automodel with this exact config. A refresh
// never retargets settings that another install (or nobody) owns.
func ownedBy(s *Object, o Options) bool {
	mine := "--config " + o.ConfigPath + " "
	if sl, ok := s.Get("statusLine"); ok {
		if slo, ok := sl.(*Object); ok {
			if c, _ := slo.Get("command"); c != nil && strings.Contains(fmt.Sprint(c)+" ", mine) {
				return true
			}
		}
	}
	b, _ := json.Marshal(s)
	return bytes.Contains(b, []byte(mine+"hook "))
}

func Refresh(o Options, cfg *config.Config) error {
	settings, raw, err := readSettings(o.SettingsPath)
	if err != nil || raw == nil {
		return err
	}
	if !ownedBy(settings, o) {
		o.Log("settings %s point at another automodel install (or none): left untouched", o.SettingsPath)
		return nil
	}
	before, _ := json.Marshal(settings)
	merge(settings, o, cfg)
	after, _ := json.Marshal(settings)
	if bytes.Equal(before, after) {
		return nil
	}
	backup := fmt.Sprintf("%s.automodel-backup-%s", o.SettingsPath, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return err
	}
	return writeSettings(o.SettingsPath, settings)
}
