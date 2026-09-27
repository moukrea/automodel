package install

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moukrea/automodel/internal/config"
)

const agentlineCmd = "bash /home/u/.claude/agentline/statusline.sh"

func withStatusline(t *testing.T, cmd string) *Object {
	t.Helper()
	s, err := ParseObject([]byte(strings.Replace(sample, "bash agentline.sh", cmd, 1)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// agentline renders the automodel segment itself: install and update leave
// its statusLine in place and never chain it.
func TestMergeKeepsAgentline(t *testing.T) {
	cfg := config.Default()
	o := Options{Exe: "/u/bin/automodel", ConfigPath: "/u/.config/automodel/config.toml"}
	s := withStatusline(t, agentlineCmd)
	merge(s, o, cfg)
	if c := statuslineCommand(s); c != agentlineCmd {
		t.Fatalf("statusLine = %q", c)
	}
	if b, _ := json.Marshal(s); !strings.Contains(string(b), `"refreshInterval":1`) || !strings.Contains(string(b), "hook decide") {
		t.Fatalf("merge: %s", b)
	}
	if c := chainable(s); c != "" {
		t.Errorf("agentline saved as statusline_command: %q", c)
	}
	// Anything else is still taken over and chained.
	s = withStatusline(t, "~/bin/my-statusline")
	if c := chainable(s); c != "~/bin/my-statusline" {
		t.Errorf("chainable = %q", c)
	}
	merge(s, o, cfg)
	if c := statuslineCommand(s); c != o.statuslineCmd() {
		t.Errorf("statusLine = %q", c)
	}
	s = NewObject()
	merge(s, o, cfg)
	if c := statuslineCommand(s); c != o.statuslineCmd() || chainable(s) != "" {
		t.Errorf("empty settings: %q", c)
	}
}

func TestRefreshKeepsAgentline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := config.Default()
	o := Options{Exe: "/u/bin/automodel", ConfigPath: "/u/.config/automodel/config.toml", SettingsPath: path, Log: func(string, ...any) {}}
	s := withStatusline(t, agentlineCmd)
	merge(s, o, cfg)
	s.Obj("env").Delete("CLAUDE_CODE_MAX_CONTEXT_TOKENS") // a new release adds an entry
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o600)
	if err := Refresh(o, cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := readSettingsOnly(path)
	if c := statuslineCommand(got); c != agentlineCmd {
		t.Fatalf("refresh clobbered agentline: %q", c)
	}
	if v, _ := got.Obj("env").Get("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v == nil {
		t.Fatal("refresh didn't re-apply its entries")
	}
}

func readSettingsOnly(path string) (*Object, error) { s, _, err := readSettings(path); return s, err }

func TestInspectSettingsStatuslineBy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := config.Default()
	o := Options{Exe: "/u/bin/automodel", ConfigPath: "/u/.config/automodel/config.toml", SettingsPath: path}
	for cmd, want := range map[string]string{agentlineCmd: "agentline", "~/bin/mine": "", "": "automodel"} {
		s := withStatusline(t, "x")
		if cmd != "" {
			s.Obj("statusLine").Set("command", cmd)
		} else {
			s.Obj("statusLine").Set("command", o.statuslineCmd())
		}
		b, _ := json.Marshal(s)
		os.WriteFile(path, b, 0o600)
		r, err := InspectSettings(o, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatuslineBy != want || r.Statusline != (want != "") {
			t.Errorf("%q: by %q (%v), want %q", cmd, r.StatuslineBy, r.Statusline, want)
		}
	}
}

// Apply (with a fake systemd) and Remove with an agentline statusLine: kept
// in place, never written into the config as statusline_command.
func TestApplyRemoveKeepAgentline(t *testing.T) {
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, http.NotFoundHandler())
	defer l.Close()
	var calls []string
	o := Options{Exe: "/u/bin/automodel", ConfigPath: filepath.Join(dir, "config.toml"), CatalogPath: filepath.Join(dir, "catalog.toml"),
		SettingsPath: filepath.Join(dir, "settings.json"), UnitPath: filepath.Join(dir, "automodel.service"),
		GOOS: "linux", Run: fakeRun(true, &calls), Log: t.Logf}
	os.WriteFile(o.ConfigPath, []byte(fmt.Sprintf("catalog = %q\nstate_dir = %q\nlisten = %q\n", o.CatalogPath, filepath.Join(dir, "state"), l.Addr())), 0o600)
	os.WriteFile(o.SettingsPath, []byte(strings.Replace(sample, "bash agentline.sh", agentlineCmd, 1)), 0o600)
	if err := Apply(o); err != nil {
		t.Fatal(err)
	}
	s, _ := readSettingsOnly(o.SettingsPath)
	if c := statuslineCommand(s); c != agentlineCmd {
		t.Fatalf("apply replaced agentline: %q", c)
	}
	if !hasHook(s.Obj("hooks"), "UserPromptSubmit", o.hookCmd("decide")) {
		t.Fatal("decide hook not installed")
	}
	if cfg, _ := os.ReadFile(o.ConfigPath); strings.Contains(string(cfg), "statusline_command") {
		t.Fatalf("agentline chained:\n%s", cfg)
	}
	if err := Remove(o); err != nil {
		t.Fatal(err)
	}
	s, _ = readSettingsOnly(o.SettingsPath)
	if c := statuslineCommand(s); c != agentlineCmd {
		t.Fatalf("remove dropped agentline: %q", c)
	}
	if b, _ := json.Marshal(s); strings.Contains(string(b), "automodel") {
		t.Fatalf("automodel entries left: %s", b)
	}
}
